package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/hatchet-dev/brickie/internal/engine"
	"github.com/hatchet-dev/brickie/internal/injector"
)

const container = "sandbox"

var exitMarker = regexp.MustCompile(`__BRICKIE_EXIT__(\d+)$`)

type InjectorOptions struct {
	Enabled    bool
	SecretName string
	Image      string
	PublicKey  string
	Port       int
}

type Options struct {
	Context      string
	Namespace    string
	Image        string
	Credentials  []string
	ReadyTimeout time.Duration
	Overlay      bool
	CSIPort      int
	Injector     InjectorOptions
}

const csiNodeLabel = "app.kubernetes.io/name=brickie-csi-node"

type Driver struct {
	opts      Options
	restCfg   *rest.Config
	client    ctrlclient.Client
	clientset *kubernetes.Clientset
}

func New(opts Options) (*Driver, error) {
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}
	if opts.Image == "" {
		opts.Image = "alpine:3"
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 5 * time.Minute
	}
	if opts.CSIPort == 0 {
		opts.CSIPort = 7078
	}
	if opts.Injector.Port == 0 {
		opts.Injector.Port = 3128
	}
	if opts.Injector.Enabled {
		if opts.Injector.PublicKey == "" || opts.Injector.SecretName == "" || opts.Injector.Image == "" {
			return nil, fmt.Errorf("injector mode requires the injector public key, secret name, and image")
		}
	}
	restCfg, err := loadConfig(opts.Context)
	if err != nil {
		return nil, err
	}
	sch := runtime.NewScheme()
	if err := scheme.AddToScheme(sch); err != nil {
		return nil, err
	}
	if err := sandboxv1beta1.AddToScheme(sch); err != nil {
		return nil, err
	}
	c, err := ctrlclient.New(restCfg, ctrlclient.Options{Scheme: sch})
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return &Driver{opts: opts, restCfg: restCfg, client: c, clientset: cs}, nil
}

func loadConfig(kubeContext string) (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil && kubeContext == "" {
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// hatchetEnv is always injected so in-sandbox harnesses can reach Hatchet
// (MCP tools that are Hatchet tasks, child task triggers, log streaming).
// Hatchet's gRPC connection cannot route through the HTTP injector, so the
// token is injected raw even in injector mode until the sidecar grows a
// Hatchet proxy that mints run-scoped tokens.
func hatchetEnv() []corev1.EnvVar {
	env := []corev1.EnvVar{}
	for _, name := range []string{"HATCHET_CLIENT_TOKEN", "HATCHET_CLIENT_HOST_PORT", "HATCHET_CLIENT_TLS_STRATEGY"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, corev1.EnvVar{Name: name, Value: v})
		}
	}
	return env
}

func mergeEnv(base, extra []corev1.EnvVar) []corev1.EnvVar {
	seen := map[string]bool{}
	for _, e := range base {
		seen[e.Name] = true
	}
	for _, e := range extra {
		if !seen[e.Name] {
			seen[e.Name] = true
			base = append(base, e)
		}
	}
	return base
}

func (d *Driver) injectedEnv() ([]corev1.EnvVar, error) {
	if !d.opts.Injector.Enabled {
		env := []corev1.EnvVar{}
		for _, name := range d.opts.Credentials {
			if v, ok := os.LookupEnv(name); ok {
				env = append(env, corev1.EnvVar{Name: name, Value: v})
			}
		}
		return mergeEnv(hatchetEnv(), env), nil
	}
	env, err := buildInjectorEnv(d.opts.Injector.PublicKey, d.opts.Injector.Port, d.opts.Credentials, os.Getenv)
	if err != nil {
		return nil, err
	}
	return mergeEnv(hatchetPlaceholderEnv(), env), nil
}

// hatchetPlaceholderEnv is the injector-mode variant of hatchetEnv: the
// sandbox gets a placeholder token (parseable, unusable) pointing at the
// sidecar's hatchet proxy; the real token lives only in the sidecar.
func hatchetPlaceholderEnv() []corev1.EnvVar {
	realToken := os.Getenv("HATCHET_CLIENT_TOKEN")
	if realToken == "" {
		return []corev1.EnvVar{}
	}
	const listen = "127.0.0.1:7077"
	placeholder, err := injector.MintHatchetPlaceholder(realToken, listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brickie: cannot mint hatchet placeholder token (%v); omitting hatchet env from sandbox\n", err)
		return []corev1.EnvVar{}
	}
	return []corev1.EnvVar{
		{Name: "HATCHET_CLIENT_TOKEN", Value: placeholder},
		{Name: "HATCHET_CLIENT_HOST_PORT", Value: listen},
		{Name: "HATCHET_CLIENT_TLS_STRATEGY", Value: "none"},
	}
}

func buildInjectorEnv(publicKey string, port int, credentials []string, getenv func(string) string) ([]corev1.EnvVar, error) {
	env := []corev1.EnvVar{}
	baseURLsSet := map[string]bool{}
	for _, name := range credentials {
		value := getenv(name)
		if value == "" {
			continue
		}
		entry, known := injector.Catalog[name]
		if !known || entry.BaseURLEnv == "" {
			fmt.Fprintf(os.Stderr, "brickie: credential %s has no injector route; injecting raw value\n", name)
			env = append(env, corev1.EnvVar{Name: name, Value: value})
			continue
		}
		sealed, err := injector.Seal(publicKey, injector.Secret{Token: value, AllowedHosts: entry.Hosts})
		if err != nil {
			return nil, fmt.Errorf("seal credential %s: %w", name, err)
		}
		env = append(env, corev1.EnvVar{Name: entry.TargetEnv, Value: sealed})
		if !baseURLsSet[entry.BaseURLEnv] {
			baseURLsSet[entry.BaseURLEnv] = true
			env = append(env, corev1.EnvVar{
				Name:  entry.BaseURLEnv,
				Value: fmt.Sprintf("http://127.0.0.1:%d/t/%s", port, entry.PrimaryHost),
			})
		}
	}
	return env, nil
}

type execResult struct {
	Stdout   []byte
	Stderr   string
	ExitCode int
}

func (d *Driver) exec(ctx context.Context, pod string, command []string, stdin []byte) (execResult, error) {
	req := d.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(d.opts.Namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(d.restCfg, "POST", req.URL())
	if err != nil {
		return execResult{}, err
	}
	var stdout, stderr bytes.Buffer
	var in io.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: in, Stdout: &stdout, Stderr: &stderr})
	res := execResult{Stdout: stdout.Bytes(), Stderr: stderr.String()}
	var codeErr k8sexec.CodeExitError
	if errors.As(err, &codeErr) {
		res.ExitCode = codeErr.Code
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("exec %s: %w\n%s", strings.Join(command, " "), err, stderr.String())
	}
	return res, nil
}

func (d *Driver) mustExec(ctx context.Context, pod string, command []string, stdin []byte) (execResult, error) {
	res, err := d.exec(ctx, pod, command, stdin)
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("exec %s failed (exit %d): %s", strings.Join(command, " "), res.ExitCode, res.Stderr)
	}
	return res, nil
}

func (d *Driver) waitReady(ctx context.Context, pod string) error {
	deadline := time.Now().Add(d.opts.ReadyTimeout)
	var last *corev1.Pod
	for time.Now().Before(deadline) {
		p, err := d.clientset.CoreV1().Pods(d.opts.Namespace).Get(ctx, pod, metav1.GetOptions{})
		if err == nil {
			last = p
			if p.Status.Phase == corev1.PodRunning {
				ready := len(p.Status.ContainerStatuses) > 0
				for _, cs := range p.Status.ContainerStatuses {
					ready = ready && cs.Ready
				}
				if ready {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("sandbox pod %s not ready within %s (%s)", pod, d.opts.ReadyTimeout, podStateSummary(last))
}

func podStateSummary(p *corev1.Pod) string {
	if p == nil {
		return "pod not found"
	}
	parts := []string{"phase=" + string(p.Status.Phase)}
	for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		switch {
		case cs.State.Waiting != nil:
			parts = append(parts, fmt.Sprintf("%s=%s: %s", cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message))
		case cs.State.Terminated != nil:
			parts = append(parts, fmt.Sprintf("%s=terminated(%s, exit %d)", cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode))
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
			parts = append(parts, "unschedulable: "+c.Message)
		}
	}
	return strings.Join(parts, "; ")
}

func (d *Driver) RunTurn(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	image := req.Image
	if image == "" {
		image = d.opts.Image
	}
	sandboxEnv, err := d.injectedEnv()
	if err != nil {
		return engine.TurnResponse{}, err
	}
	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:       container,
			Image:      image,
			Command:    []string{"sleep", "infinity"},
			Env:        sandboxEnv,
			WorkingDir: engine.Workspace,
		}},
	}
	if d.opts.Injector.Enabled {
		sidecarRestart := corev1.ContainerRestartPolicyAlways
		automount := false
		podSpec.AutomountServiceAccountToken = &automount
		sidecarEnv := []corev1.EnvVar{}
		for _, name := range []string{"HATCHET_CLIENT_TOKEN", "HATCHET_CLIENT_TLS_STRATEGY"} {
			if v, ok := os.LookupEnv(name); ok {
				sidecarEnv = append(sidecarEnv, corev1.EnvVar{Name: name, Value: v})
			}
		}
		podSpec.InitContainers = []corev1.Container{{
			Name:          "injector",
			Image:         d.opts.Injector.Image,
			Args:          []string{"injector", "serve", "--listen", fmt.Sprintf("127.0.0.1:%d", d.opts.Injector.Port)},
			RestartPolicy: &sidecarRestart,
			Env:           sidecarEnv,
			EnvFrom: []corev1.EnvFromSource{{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: d.opts.Injector.SecretName},
				},
			}},
		}}
	}
	if d.opts.Overlay {
		podSpec.Volumes = []corev1.Volume{{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver: "csi.brickie.hatchet.run",
					VolumeAttributes: map[string]string{
						"brickie.hatchet.run/sandbox-id":  req.SandboxName,
						"brickie.hatchet.run/snapshot-id": req.SnapshotID,
					},
				},
			},
		}}
		podSpec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
			Name:      "workspace",
			MountPath: engine.Workspace,
		}}
	}
	sb := &sandboxv1beta1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: req.SandboxName, Namespace: d.opts.Namespace},
		Spec: sandboxv1beta1.SandboxSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{Spec: podSpec},
			},
		},
	}
	if err := d.client.Create(ctx, sb); err != nil {
		return engine.TurnResponse{}, fmt.Errorf("create sandbox: %w", err)
	}
	req.Logf("sandbox %s created in namespace %s", req.SandboxName, d.opts.Namespace)
	defer func() {
		_ = d.client.Delete(context.Background(), sb)
		req.Logf("sandbox %s deleted", req.SandboxName)
	}()

	waitStart := time.Now()
	if err := d.waitReady(ctx, req.SandboxName); err != nil {
		req.Logf("sandbox %s not ready: %v", req.SandboxName, err)
		return engine.TurnResponse{}, err
	}
	req.Logf("sandbox %s ready after %s", req.SandboxName, time.Since(waitStart).Round(time.Millisecond))
	if !d.opts.Overlay {
		if _, err := d.mustExec(ctx, req.SandboxName, []string{"mkdir", "-p", engine.Workspace}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
		if req.SnapshotTar != nil {
			if _, err := d.mustExec(ctx, req.SandboxName, []string{"tar", "-xzf", "-", "-C", engine.Workspace}, req.SnapshotTar); err != nil {
				return engine.TurnResponse{}, fmt.Errorf("restore snapshot: %w", err)
			}
			req.Logf("workspace restored from snapshot tar")
		}
	}

	execStart := time.Now()
	var output any
	switch req.Fn {
	case engine.CommandFn:
		var in engine.CommandInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return engine.TurnResponse{}, err
		}
		script := fmt.Sprintf("cd %s && { %s\n}; printf '__BRICKIE_EXIT__%%s' $?", engine.Workspace, in.Cmd)
		res, err := d.exec(ctx, req.SandboxName, []string{"sh", "-c", script}, nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		raw := string(res.Stdout)
		code := -1
		if m := exitMarker.FindStringSubmatch(raw); m != nil {
			code, _ = strconv.Atoi(m[1])
			raw = exitMarker.ReplaceAllString(raw, "")
		}
		output = engine.CommandOutput{Stdout: strings.TrimSpace(raw), Stderr: strings.TrimSpace(res.Stderr), ExitCode: code}
	case engine.PromptFn:
		var in engine.PromptInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := d.exec(ctx, req.SandboxName, engine.PromptCommand(engine.Workspace, in.Prompt), nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		if res.ExitCode != 0 {
			return engine.TurnResponse{}, fmt.Errorf("claude failed (exit %d): %s", res.ExitCode, res.Stderr)
		}
		ls, err := d.mustExec(ctx, req.SandboxName, []string{"ls", "-1", engine.Workspace}, nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = engine.PromptOutput{Reply: engine.ParsePromptReply(string(res.Stdout)), Files: engine.FilterFiles(strings.Split(strings.TrimSpace(string(ls.Stdout)), "\n"))}
	default:
		if req.FnSource == "" {
			return engine.TurnResponse{}, fmt.Errorf("unknown sandbox fn %q and no fnSource provided", req.Fn)
		}
		ctrl := engine.Workspace + "/.brickie"
		if _, err := d.mustExec(ctx, req.SandboxName, []string{"mkdir", "-p", ctrl}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
		if _, err := d.mustExec(ctx, req.SandboxName, []string{"sh", "-c", "cat > " + ctrl + "/runner.cjs"}, []byte(engine.RunnerJS)); err != nil {
			return engine.TurnResponse{}, err
		}
		if _, err := d.mustExec(ctx, req.SandboxName, []string{"sh", "-c", "cat > " + ctrl + "/input.json"}, engine.RunnerInput(req.FnSource, req.Input)); err != nil {
			return engine.TurnResponse{}, err
		}
		if _, err := d.mustExec(ctx, req.SandboxName, []string{"node", ctrl + "/runner.cjs"}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := d.mustExec(ctx, req.SandboxName, []string{"cat", ctrl + "/output.json"}, nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = json.RawMessage(res.Stdout)
		if _, err := d.mustExec(ctx, req.SandboxName, []string{"rm", "-rf", ctrl}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
	}

	req.Logf("fn %s executed in %s", req.Fn, time.Since(execStart).Round(time.Millisecond))
	outJSON, err := json.Marshal(output)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	if d.opts.Overlay {
		if err := d.checkpoint(ctx, req.SandboxName, req.CheckpointID); err != nil {
			req.Logf("overlay checkpoint %s failed: %v", req.CheckpointID, err)
			return engine.TurnResponse{}, err
		}
		req.Logf("overlay checkpoint %s complete", req.CheckpointID)
		return engine.TurnResponse{Output: outJSON}, nil
	}
	tar, err := d.mustExec(ctx, req.SandboxName, []string{"tar", "-czf", "-", "-C", engine.Workspace, "."}, nil)
	if err != nil {
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace: %w", err)
	}
	return engine.TurnResponse{Output: outJSON, SnapshotTar: tar.Stdout}, nil
}

func (d *Driver) SnapshotNative() bool {
	return d.opts.Overlay
}

// checkpoint asks the CSI node daemon on the sandbox pod's node to freeze the
// workspace upper layer as the snapshot named by checkpointID.
func (d *Driver) checkpoint(ctx context.Context, sandboxName, checkpointID string) error {
	pod, err := d.clientset.CoreV1().Pods(d.opts.Namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("resolve sandbox pod node: %w", err)
	}
	nodes, err := d.clientset.CoreV1().Pods(d.opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: csiNodeLabel,
		FieldSelector: "spec.nodeName=" + pod.Spec.NodeName,
	})
	if err != nil {
		return fmt.Errorf("find csi node pod on %s: %w", pod.Spec.NodeName, err)
	}
	if len(nodes.Items) == 0 || nodes.Items[0].Status.PodIP == "" {
		return fmt.Errorf("no running brickie csi node pod on node %s (is csi.enabled set?)", pod.Spec.NodeName)
	}
	body, _ := json.Marshal(map[string]string{"sandboxId": sandboxName, "snapshotId": checkpointID})
	url := fmt.Sprintf("http://%s:%d/v1/checkpoint", nodes.Items[0].Status.PodIP, d.opts.CSIPort)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("checkpoint %s: %w", sandboxName, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("checkpoint %s failed (%d): %s", sandboxName, res.StatusCode, detail)
	}
	return nil
}
