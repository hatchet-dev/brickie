package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extensionsv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
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
	GVisor       string
	WarmImages   []string
	WarmReplicas int
}

const gvisorRuntimeClass = "gvisor"

const csiNodeLabel = "app.kubernetes.io/name=brickie-csi-node"

type Driver struct {
	opts      Options
	restCfg   *rest.Config
	client    ctrlclient.Client
	clientset *kubernetes.Clientset
	gvisor    bool
	poolMu    sync.Mutex
	pools     map[string]string
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
	if opts.WarmReplicas == 0 {
		opts.WarmReplicas = 2
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
	if err := extensionsv1beta1.AddToScheme(sch); err != nil {
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
	d := &Driver{opts: opts, restCfg: restCfg, client: c, clientset: cs, pools: map[string]string{}}
	if err := d.detectGVisor(); err != nil {
		return nil, err
	}
	if err := d.storeHatchetTokenForSidecar(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, image := range opts.WarmImages {
		if _, err := d.ensurePool(ctx, image); err != nil {
			return nil, fmt.Errorf("prewarm %s: %w", image, err)
		}
	}
	return d, nil
}

// The sidecar reads the real Hatchet token from the injector Secret via its
// envFrom, so the token never appears inline in a pod spec.
func (d *Driver) storeHatchetTokenForSidecar() error {
	if !d.opts.Injector.Enabled {
		return nil
	}
	token := os.Getenv("HATCHET_CLIENT_TOKEN")
	if token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	secret, err := d.clientset.CoreV1().Secrets(d.opts.Namespace).Get(ctx, d.opts.Injector.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read injector secret %s: %w", d.opts.Injector.SecretName, err)
	}
	if string(secret.Data["HATCHET_CLIENT_TOKEN"]) == token {
		return nil
	}
	patch := []byte(fmt.Sprintf(`{"stringData":{"HATCHET_CLIENT_TOKEN":%q}}`, token))
	if _, err := d.clientset.CoreV1().Secrets(d.opts.Namespace).Patch(ctx, d.opts.Injector.SecretName, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("store hatchet token in injector secret %s: %w", d.opts.Injector.SecretName, err)
	}
	return nil
}

func (d *Driver) detectGVisor() error {
	mode := d.opts.GVisor
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "off":
		return nil
	case "auto", "on":
	default:
		return fmt.Errorf("unknown gvisor mode %q (auto|on|off)", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := d.clientset.NodeV1().RuntimeClasses().Get(ctx, gvisorRuntimeClass, metav1.GetOptions{})
	if err == nil {
		d.gvisor = true
		fmt.Fprintf(os.Stderr, "brickie: gvisor runtime class found; sandboxes run under gVisor\n")
		return nil
	}
	if mode == "on" {
		return fmt.Errorf("gvisor mode is on but the %q runtime class is unavailable (%v): install runsc on the nodes and create the runtime class, or run brickie bootstrap-aws", gvisorRuntimeClass, err)
	}
	fmt.Fprintf(os.Stderr, "brickie: gvisor runtime class not found (%v); sandboxes run with the default runtime\n", err)
	return nil
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

// buildPodSpec is the whole sandbox pod shape. Warm pool templates are built
// from it, so nothing in it may vary per turn: the workspace CSI volume
// carries no attributes and the CSI node plugin keys the mount by pod name.
func (d *Driver) buildPodSpec(image string) (corev1.PodSpec, error) {
	sandboxEnv, err := d.injectedEnv()
	if err != nil {
		return corev1.PodSpec{}, err
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
	automount := false
	podSpec.AutomountServiceAccountToken = &automount
	if d.opts.Injector.Enabled {
		sidecarRestart := corev1.ContainerRestartPolicyAlways
		sidecarEnv := []corev1.EnvVar{}
		if v, ok := os.LookupEnv("HATCHET_CLIENT_TLS_STRATEGY"); ok {
			sidecarEnv = append(sidecarEnv, corev1.EnvVar{Name: "HATCHET_CLIENT_TLS_STRATEGY", Value: v})
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
				CSI: &corev1.CSIVolumeSource{Driver: "csi.brickie.hatchet.run"},
			},
		}}
		podSpec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
			Name:      "workspace",
			MountPath: engine.Workspace,
		}}
	}
	if d.gvisor {
		rc := gvisorRuntimeClass
		podSpec.RuntimeClassName = &rc
	}
	return podSpec, nil
}

func warmPoolName(image string) string {
	return "brickie-warm-" + fmt.Sprintf("%x", sha256.Sum256([]byte(image)))[:16]
}

// ensurePool upserts the SandboxTemplate and SandboxWarmPool for an image the
// first time it is seen. The pool's Recreate strategy replaces unclaimed warm
// pods whenever the template changes (image default, credentials, injector).
func (d *Driver) ensurePool(ctx context.Context, image string) (string, error) {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	if name, ok := d.pools[image]; ok {
		return name, nil
	}
	podSpec, err := d.buildPodSpec(image)
	if err != nil {
		return "", err
	}
	name := warmPoolName(image)
	tmpl := &extensionsv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.opts.Namespace},
		Spec: extensionsv1beta1.SandboxTemplateSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{Spec: podSpec},
			},
			NetworkPolicyManagement: extensionsv1beta1.NetworkPolicyManagementUnmanaged,
		},
	}
	replicas := int32(d.opts.WarmReplicas)
	pool := &extensionsv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.opts.Namespace},
		Spec: extensionsv1beta1.SandboxWarmPoolSpec{
			Replicas:    &replicas,
			TemplateRef: extensionsv1beta1.SandboxTemplateRef{Name: name},
			UpdateStrategy: &extensionsv1beta1.SandboxWarmPoolUpdateStrategy{
				Type: extensionsv1beta1.RecreateSandboxWarmPoolUpdateStrategyType,
			},
		},
	}
	if err := d.upsert(ctx, tmpl, func(existing, desired ctrlclient.Object) {
		existing.(*extensionsv1beta1.SandboxTemplate).Spec = desired.(*extensionsv1beta1.SandboxTemplate).Spec
	}); err != nil {
		return "", fmt.Errorf("ensure sandbox template for %s: %w", image, err)
	}
	if err := d.upsert(ctx, pool, func(existing, desired ctrlclient.Object) {
		existing.(*extensionsv1beta1.SandboxWarmPool).Spec = desired.(*extensionsv1beta1.SandboxWarmPool).Spec
	}); err != nil {
		return "", fmt.Errorf("ensure warm pool for %s: %w", image, err)
	}
	d.pools[image] = name
	fmt.Fprintf(os.Stderr, "brickie: warm pool %s serving image %s (%d replicas)\n", name, image, d.opts.WarmReplicas)
	return name, nil
}

func (d *Driver) upsert(ctx context.Context, obj ctrlclient.Object, copySpec func(existing, desired ctrlclient.Object)) error {
	err := d.client.Create(ctx, obj)
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing := obj.DeepCopyObject().(ctrlclient.Object)
	if err := d.client.Get(ctx, ctrlclient.ObjectKeyFromObject(obj), existing); err != nil {
		return err
	}
	copySpec(existing, obj)
	return d.client.Update(ctx, existing)
}

// claim checks a sandbox out of the image's warm pool. Retried runs reuse the
// claim name, so a stale claim from a failed attempt is deleted first and a
// fresh warm pod is bound.
func (d *Driver) claim(ctx context.Context, pool, name string) (string, func(), error) {
	newClaim := func() *extensionsv1beta1.SandboxClaim {
		return &extensionsv1beta1.SandboxClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.opts.Namespace},
			Spec: extensionsv1beta1.SandboxClaimSpec{
				WarmPoolRef: extensionsv1beta1.SandboxWarmPoolRef{Name: pool},
			},
		}
	}
	claim := newClaim()
	err := d.client.Create(ctx, claim)
	if apierrors.IsAlreadyExists(err) {
		if err := d.client.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
			return "", nil, fmt.Errorf("delete stale claim %s: %w", name, err)
		}
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			var existing extensionsv1beta1.SandboxClaim
			if apierrors.IsNotFound(d.client.Get(ctx, ctrlclient.ObjectKeyFromObject(claim), &existing)) {
				break
			}
			select {
			case <-ctx.Done():
				return "", nil, ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		claim = newClaim()
		err = d.client.Create(ctx, claim)
	}
	if err != nil {
		return "", nil, fmt.Errorf("create sandbox claim %s: %w", name, err)
	}
	cleanup := func() {
		_ = d.client.Delete(context.Background(), claim)
	}
	deadline := time.Now().Add(d.opts.ReadyTimeout)
	var last *extensionsv1beta1.SandboxClaim
	for time.Now().Before(deadline) {
		var current extensionsv1beta1.SandboxClaim
		if err := d.client.Get(ctx, ctrlclient.ObjectKeyFromObject(claim), &current); err == nil {
			last = &current
			if current.Status.SandboxStatus.Name != "" && meta.IsStatusConditionTrue(current.Status.Conditions, "Ready") {
				return current.Status.SandboxStatus.Name, cleanup, nil
			}
		}
		select {
		case <-ctx.Done():
			cleanup()
			return "", nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	detail := "claim never came back from the api server"
	if last != nil {
		if c := meta.FindStatusCondition(last.Status.Conditions, "Ready"); c != nil {
			detail = fmt.Sprintf("%s: %s", c.Reason, c.Message)
		}
	}
	cleanup()
	return "", nil, fmt.Errorf("sandbox claim %s not bound within %s (%s)", name, d.opts.ReadyTimeout, detail)
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
	if codeErr, ok := errors.AsType[k8sexec.CodeExitError](err); ok {
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

func (d *Driver) podWarnings(ctx context.Context, pod string) string {
	events, err := d.clientset.CoreV1().Events(d.opts.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + pod + ",type=Warning",
	})
	if err != nil || len(events.Items) == 0 {
		return ""
	}
	items := events.Items
	if len(items) > 3 {
		items = items[len(items)-3:]
	}
	msgs := make([]string, 0, len(items))
	for _, e := range items {
		msgs = append(msgs, e.Reason+": "+e.Message)
	}
	return strings.Join(msgs, "; ")
}

func (d *Driver) waitReady(ctx context.Context, pod string) error {
	deadline := time.Now().Add(d.opts.ReadyTimeout)
	var last *corev1.Pod
	for time.Now().Before(deadline) {
		p, err := d.clientset.CoreV1().Pods(d.opts.Namespace).Get(ctx, pod, metav1.GetOptions{})
		if err == nil && p.DeletionTimestamp == nil {
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
			if p.Status.Phase == corev1.PodPending {
				if w := d.podWarnings(ctx, pod); isRuntimeHandlerError(errors.New(w)) {
					return fmt.Errorf("sandbox pod %s cannot start: %s", pod, w)
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	detail := podStateSummary(last)
	if w := d.podWarnings(ctx, pod); w != "" {
		detail += "; " + w
	}
	return fmt.Errorf("sandbox pod %s not ready within %s (%s)", pod, d.opts.ReadyTimeout, detail)
}

func isRuntimeHandlerError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "runsc") || strings.Contains(msg, "runtime handler")
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
	pool, err := d.ensurePool(ctx, image)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	claimStart := time.Now()
	pod, cleanup, err := d.claim(ctx, pool, req.SandboxName)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	defer func() {
		cleanup()
		req.Logf("sandbox claim %s released (pod %s)", req.SandboxName, pod)
	}()
	if err := d.waitReady(ctx, pod); err != nil {
		req.Logf("sandbox %s not ready: %v", pod, err)
		return engine.TurnResponse{}, err
	}
	req.Logf("sandbox %s claimed from pool %s in %s", pod, pool, time.Since(claimStart).Round(time.Millisecond))

	if d.opts.Overlay {
		if req.SnapshotID != "" {
			restoreStart := time.Now()
			if err := d.csiPost(ctx, pod, "restore", req.SnapshotID); err != nil {
				return engine.TurnResponse{}, err
			}
			req.Logf("snapshot %s restored into %s in %s", req.SnapshotID, pod, time.Since(restoreStart).Round(time.Millisecond))
		}
	} else {
		if _, err := d.mustExec(ctx, pod, []string{"mkdir", "-p", engine.Workspace}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
		if req.SnapshotTar != nil {
			if _, err := d.mustExec(ctx, pod, []string{"tar", "-xzf", "-", "-C", engine.Workspace}, req.SnapshotTar); err != nil {
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
		res, err := d.exec(ctx, pod, []string{"sh", "-c", script}, nil)
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
		res, err := d.exec(ctx, pod, engine.PromptCommand(engine.Workspace, in.Prompt), nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		if res.ExitCode != 0 {
			return engine.TurnResponse{}, fmt.Errorf("claude failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr+string(res.Stdout)))
		}
		ls, err := d.mustExec(ctx, pod, []string{"ls", "-1", engine.Workspace}, nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = engine.PromptOutput{Reply: engine.ParsePromptReply(string(res.Stdout)), Files: engine.FilterFiles(strings.Split(strings.TrimSpace(string(ls.Stdout)), "\n"))}
	default:
		if req.FnSource == "" {
			return engine.TurnResponse{}, fmt.Errorf("unknown sandbox fn %q and no fnSource provided", req.Fn)
		}
		ctrl := engine.Workspace + "/.brickie"
		if _, err := d.mustExec(ctx, pod, []string{"mkdir", "-p", ctrl}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
		if _, err := d.mustExec(ctx, pod, []string{"sh", "-c", "cat > " + ctrl + "/runner.cjs"}, []byte(engine.RunnerJS)); err != nil {
			return engine.TurnResponse{}, err
		}
		if _, err := d.mustExec(ctx, pod, []string{"sh", "-c", "cat > " + ctrl + "/input.json"}, engine.RunnerInput(req.FnSource, req.Input)); err != nil {
			return engine.TurnResponse{}, err
		}
		if _, err := d.mustExec(ctx, pod, []string{"node", ctrl + "/runner.cjs"}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := d.mustExec(ctx, pod, []string{"cat", ctrl + "/output.json"}, nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = json.RawMessage(res.Stdout)
		if _, err := d.mustExec(ctx, pod, []string{"rm", "-rf", ctrl}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
	}

	req.Logf("fn %s executed in %s", req.Fn, time.Since(execStart).Round(time.Millisecond))
	outJSON, err := json.Marshal(output)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	if d.opts.Overlay {
		if _, err := d.exec(ctx, pod, []string{"sh", "-c", "sync"}, nil); err != nil {
			req.Logf("pre-checkpoint sync failed: %v", err)
		}
		if err := d.csiPost(ctx, pod, "checkpoint", req.CheckpointID); err != nil {
			req.Logf("overlay checkpoint %s failed: %v", req.CheckpointID, err)
			return engine.TurnResponse{}, err
		}
		req.Logf("overlay checkpoint %s complete", req.CheckpointID)
		return engine.TurnResponse{Output: outJSON}, nil
	}
	tar, err := d.mustExec(ctx, pod, []string{"tar", "-czf", "-", "-C", engine.Workspace, "."}, nil)
	if err != nil {
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace: %w", err)
	}
	return engine.TurnResponse{Output: outJSON, SnapshotTar: tar.Stdout}, nil
}

func (d *Driver) SnapshotNative() bool {
	return d.opts.Overlay
}

// csiPost asks the CSI node daemon on the sandbox pod's node to checkpoint or
// restore the pod's live workspace. The workspace is keyed by pod name via
// the CSI driver's podInfoOnMount.
func (d *Driver) csiPost(ctx context.Context, pod, op, snapshotID string) error {
	p, err := d.clientset.CoreV1().Pods(d.opts.Namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("resolve sandbox pod node: %w", err)
	}
	nodes, err := d.clientset.CoreV1().Pods(d.opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: csiNodeLabel,
		FieldSelector: "spec.nodeName=" + p.Spec.NodeName,
	})
	if err != nil {
		return fmt.Errorf("find csi node pod on %s: %w", p.Spec.NodeName, err)
	}
	if len(nodes.Items) == 0 || nodes.Items[0].Status.PodIP == "" {
		return fmt.Errorf("no running brickie csi node pod on node %s (is csi.enabled set?)", p.Spec.NodeName)
	}
	body, _ := json.Marshal(map[string]string{"sandboxId": pod, "snapshotId": snapshotID})
	url := fmt.Sprintf("http://%s:%d/v1/%s", nodes.Items[0].Status.PodIP, d.opts.CSIPort, op)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, pod, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("%s %s failed (%d): %s", op, pod, res.StatusCode, detail)
	}
	return nil
}
