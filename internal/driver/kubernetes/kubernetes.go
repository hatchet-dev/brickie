// Package kubernetes runs sandboxes as agent-sandbox pods, one per session.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

const container = "sandbox"

var exitMarker = regexp.MustCompile(`__BRICKIE_EXIT__(\d+)$`)

// Options configures the driver; zero values take the documented defaults.
type Options struct {
	Context      string
	Namespace    string
	Image        string
	Credentials  []string
	ReadyTimeout time.Duration
	IdleTTL      time.Duration
	Runtime      string
	WarmImages   []string
	WarmReplicas int
}

const (
	gvisorRuntimeClass = "gvisor"
	kataRuntimeClass   = "kata-clh"
)

var runtimeClasses = map[string]string{"gvisor": gvisorRuntimeClass, "kata": kataRuntimeClass}

const podUIDAnnotation = "brickie.hatchet.run/pod-uid"

// Driver is the kubernetes sandbox driver.
type Driver struct {
	opts         Options
	restCfg      *rest.Config
	client       ctrlclient.Client
	clientset    *kubernetes.Clientset
	runtimeClass string
	poolMu       sync.Mutex
	pools        map[string]string
	sessionMu    sync.Mutex
	sessions     map[string]*sync.Mutex
}

// New connects to the cluster, selects the runtime class, and warms the configured pools.
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
	if opts.IdleTTL == 0 {
		opts.IdleTTL = 30 * time.Minute
	}
	if opts.WarmReplicas == 0 {
		opts.WarmReplicas = 2
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
	d := &Driver{opts: opts, restCfg: restCfg, client: c, clientset: cs, pools: map[string]string{}, sessions: map[string]*sync.Mutex{}}
	if err := d.detectRuntime(); err != nil {
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

func (d *Driver) detectRuntime() error {
	mode := d.opts.Runtime
	if mode == "" {
		mode = "auto"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	has := func(name string) bool {
		_, err := d.clientset.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
		return err == nil
	}
	switch mode {
	case "none":
		return nil
	case "auto":
		for _, rc := range []string{kataRuntimeClass, gvisorRuntimeClass} {
			if has(rc) {
				d.runtimeClass = rc
				break
			}
		}
		if d.runtimeClass == "" {
			fmt.Fprintf(os.Stderr, "brickie: no %s or %s runtime class; sandboxes run with the default runtime\n", kataRuntimeClass, gvisorRuntimeClass)
			return nil
		}
	default:
		rc, ok := runtimeClasses[mode]
		if !ok {
			return fmt.Errorf("unknown runtime %q (auto|gvisor|kata|none)", mode)
		}
		if !has(rc) {
			return fmt.Errorf("runtime %s requested but the %q runtime class is unavailable on this cluster", mode, rc)
		}
		d.runtimeClass = rc
	}
	fmt.Fprintf(os.Stderr, "brickie: sandboxes run under runtime class %s\n", d.runtimeClass)
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

// The sandbox gets the Hatchet connection so in-sandbox harnesses can reach
// Hatchet, plus the allowlisted credential variables from the provider process.
func (d *Driver) sandboxEnv() []corev1.EnvVar {
	env := []corev1.EnvVar{}
	names := append([]string{"HATCHET_CLIENT_TOKEN", "HATCHET_CLIENT_HOST_PORT", "HATCHET_CLIENT_TLS_STRATEGY"}, d.opts.Credentials...)
	seen := map[string]bool{}
	for _, name := range names {
		if v, ok := os.LookupEnv(name); ok && !seen[name] {
			seen[name] = true
			env = append(env, corev1.EnvVar{Name: name, Value: v})
		}
	}
	return env
}

// buildPodSpec is the whole sandbox pod shape. Warm pool templates are built
// from it, so nothing in it may vary per session.
func (d *Driver) buildPodSpec(image string) corev1.PodSpec {
	automount := false
	podSpec := corev1.PodSpec{
		AutomountServiceAccountToken: &automount,
		Containers: []corev1.Container{{
			Name:       container,
			Image:      image,
			Command:    []string{"sleep", "infinity"},
			Env:        d.sandboxEnv(),
			WorkingDir: engine.Workspace,
		}},
	}
	if d.runtimeClass != "" {
		rc := d.runtimeClass
		podSpec.RuntimeClassName = &rc
	}
	return podSpec
}

func warmPoolName(image string) string {
	return "brickie-warm-" + fmt.Sprintf("%x", sha256.Sum256([]byte(image)))[:16]
}

// ensurePool upserts the SandboxTemplate and SandboxWarmPool for an image the
// first time it is seen. The pool's Recreate strategy replaces unclaimed warm
// pods whenever the template changes.
func (d *Driver) ensurePool(ctx context.Context, image string) (string, error) {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	if name, ok := d.pools[image]; ok {
		return name, nil
	}
	name := warmPoolName(image)
	tmpl := &extensionsv1beta1.SandboxTemplate{
		Name: name, Namespace: d.opts.Namespace,
		Spec: extensionsv1beta1.SandboxTemplateSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{Spec: d.buildPodSpec(image)},
			},
			NetworkPolicyManagement: extensionsv1beta1.NetworkPolicyManagementUnmanaged,
		},
	}
	replicas := int32(min(d.opts.WarmReplicas, 1000)) // #nosec G115 -- bounded above
	pool := &extensionsv1beta1.SandboxWarmPool{
		Name: name, Namespace: d.opts.Namespace,
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

func (d *Driver) shutdownTime() *metav1.Time {
	t := metav1.NewTime(time.Now().Add(d.opts.IdleTTL))
	return &t
}

func (d *Driver) claimKey(name string) ctrlclient.ObjectKey {
	return ctrlclient.ObjectKey{Namespace: d.opts.Namespace, Name: name}
}

func (d *Driver) patchClaim(ctx context.Context, name string, patch map[string]any) error {
	body, _ := json.Marshal(patch)
	claim := &extensionsv1beta1.SandboxClaim{Name: name, Namespace: d.opts.Namespace}
	return d.client.Patch(ctx, claim, ctrlclient.RawPatch(types.MergePatchType, body))
}

func (d *Driver) ensureClaim(ctx context.Context, pool, name string) (*extensionsv1beta1.SandboxClaim, error) {
	newClaim := func() *extensionsv1beta1.SandboxClaim {
		return &extensionsv1beta1.SandboxClaim{
			Name: name, Namespace: d.opts.Namespace,
			Spec: extensionsv1beta1.SandboxClaimSpec{
				WarmPoolRef: extensionsv1beta1.SandboxWarmPoolRef{Name: pool},
				Lifecycle: &extensionsv1beta1.Lifecycle{
					ShutdownTime:   d.shutdownTime(),
					ShutdownPolicy: extensionsv1beta1.ShutdownPolicyDelete,
				},
			},
		}
	}
	err := d.client.Create(ctx, newClaim())
	if apierrors.IsAlreadyExists(err) {
		var existing extensionsv1beta1.SandboxClaim
		if err := d.client.Get(ctx, d.claimKey(name), &existing); err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		if existing.DeletionTimestamp != nil {
			if err := d.waitClaimGone(ctx, name); err != nil {
				return nil, err
			}
			err = d.client.Create(ctx, newClaim())
		} else {
			err = nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create sandbox claim %s: %w", name, err)
	}
	deadline := time.Now().Add(d.opts.ReadyTimeout)
	var last *extensionsv1beta1.SandboxClaim
	for time.Now().Before(deadline) {
		var current extensionsv1beta1.SandboxClaim
		if err := d.client.Get(ctx, d.claimKey(name), &current); err == nil {
			last = &current
			if current.Status.SandboxStatus.Name != "" && meta.IsStatusConditionTrue(current.Status.Conditions, "Ready") {
				return &current, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	detail := "claim never came back from the api server"
	if last != nil {
		if c := meta.FindStatusCondition(last.Status.Conditions, "Ready"); c != nil {
			detail = fmt.Sprintf("%s: %s", c.Reason, c.Message)
		}
	}
	return nil, fmt.Errorf("sandbox claim %s not bound within %s (%s)", name, d.opts.ReadyTimeout, detail)
}

func (d *Driver) waitClaimGone(ctx context.Context, name string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var existing extensionsv1beta1.SandboxClaim
		if apierrors.IsNotFound(d.client.Get(ctx, d.claimKey(name), &existing)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("previous sandbox claim %s is still terminating", name)
}

func (d *Driver) resolveSession(ctx context.Context, name string) (*corev1.Pod, error) {
	var claim extensionsv1beta1.SandboxClaim
	err := d.client.Get(ctx, d.claimKey(name), &claim)
	gone := fmt.Errorf("sandbox %s is gone (expired after idle or destroyed); create a new sandbox from its last snapshot", name)
	switch {
	case apierrors.IsNotFound(err):
		return nil, gone
	case err != nil:
		return nil, err
	case claim.DeletionTimestamp != nil, claim.Status.SandboxStatus.Name == "", !meta.IsStatusConditionTrue(claim.Status.Conditions, "Ready"):
		return nil, gone
	}
	pod, err := d.clientset.CoreV1().Pods(d.opts.Namespace).Get(ctx, claim.Status.SandboxStatus.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, gone
	}
	if err != nil {
		return nil, err
	}
	if string(pod.UID) != claim.Annotations[podUIDAnnotation] {
		return nil, fmt.Errorf("sandbox %s was replaced (pod %s restarted); its live workspace is lost, create a new sandbox from its last snapshot", name, pod.Name)
	}
	if err := d.patchClaim(ctx, name, map[string]any{"spec": map[string]any{"lifecycle": map[string]any{"shutdownTime": d.shutdownTime()}}}); err != nil {
		return nil, fmt.Errorf("extend sandbox %s deadline: %w", name, err)
	}
	return pod, nil
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

func (d *Driver) waitReady(ctx context.Context, pod string) (*corev1.Pod, error) {
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
					return p, nil
				}
			}
			if p.Status.Phase == corev1.PodPending {
				if w := d.podWarnings(ctx, pod); isRuntimeHandlerError(w) {
					return nil, fmt.Errorf("sandbox pod %s cannot start: %s", pod, w)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	detail := podStateSummary(last)
	if w := d.podWarnings(ctx, pod); w != "" {
		detail += "; " + w
	}
	return nil, fmt.Errorf("sandbox pod %s not ready within %s (%s)", pod, d.opts.ReadyTimeout, detail)
}

func isRuntimeHandlerError(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "runsc") || strings.Contains(msg, "kata") || strings.Contains(msg, "runtime handler")
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

func (d *Driver) sessionLock(name string) *sync.Mutex {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	mu, ok := d.sessions[name]
	if !ok {
		mu = &sync.Mutex{}
		d.sessions[name] = mu
	}
	return mu
}

// RunTurn runs one turn; turns on the same sandbox are serialized.
func (d *Driver) RunTurn(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	mu := d.sessionLock(req.SandboxName)
	mu.Lock()
	defer mu.Unlock()
	switch req.Fn {
	case engine.CreateFn:
		return d.create(ctx, req)
	case engine.DestroyFn:
		return d.destroy(ctx, req)
	default:
		return d.run(ctx, req)
	}
}

func newTimings() (map[string]int64, func(string, time.Time)) {
	timings := map[string]int64{}
	return timings, func(name string, start time.Time) {
		timings[name] = time.Since(start).Milliseconds()
	}
}

func (d *Driver) create(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	image := req.Image
	if image == "" {
		image = d.opts.Image
	}
	timings, phase := newTimings()
	pool, err := d.ensurePool(ctx, image)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	claimStart := time.Now()
	claim, err := d.ensureClaim(ctx, pool, req.SandboxName)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	pod, err := d.waitReady(ctx, claim.Status.SandboxStatus.Name)
	if err != nil {
		req.Logf("sandbox %s not ready: %v", claim.Status.SandboxStatus.Name, err)
		return engine.TurnResponse{}, err
	}
	phase("claim", claimStart)
	uid := claim.Annotations[podUIDAnnotation]
	reused := uid != ""
	if reused && uid != string(pod.UID) {
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s was replaced (pod %s restarted) before its first turn; destroy it and create again", req.SandboxName, pod.Name)
	}
	if reused {
		req.Logf("sandbox %s reused (pod %s)", req.SandboxName, pod.Name)
	} else {
		req.Logf("sandbox %s claimed from pool %s in %s (pod %s)", req.SandboxName, pool, time.Since(claimStart).Round(time.Millisecond), pod.Name)
		if _, err := d.mustExec(ctx, pod.Name, []string{"mkdir", "-p", engine.Workspace}, nil); err != nil {
			return engine.TurnResponse{}, err
		}
		if req.SnapshotTar != nil {
			restoreStart := time.Now()
			if _, err := d.mustExec(ctx, pod.Name, []string{"tar", "-xzf", "-", "-C", engine.Workspace}, req.SnapshotTar); err != nil {
				return engine.TurnResponse{}, fmt.Errorf("restore snapshot: %w", err)
			}
			phase("restore", restoreStart)
			req.Logf("snapshot %s restored into %s in %s", req.SnapshotID, pod.Name, time.Since(restoreStart).Round(time.Millisecond))
		}
	}
	if err := d.patchClaim(ctx, req.SandboxName, map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{podUIDAnnotation: string(pod.UID)}},
		"spec":     map[string]any{"lifecycle": map[string]any{"shutdownTime": d.shutdownTime()}},
	}); err != nil {
		return engine.TurnResponse{}, fmt.Errorf("record sandbox pod: %w", err)
	}
	out, _ := json.Marshal(map[string]bool{"created": true, "reused": reused})
	return engine.TurnResponse{Output: out, Timings: timings}, nil
}

func (d *Driver) destroy(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	claim := &extensionsv1beta1.SandboxClaim{Name: req.SandboxName, Namespace: d.opts.Namespace}
	if err := d.client.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
		return engine.TurnResponse{}, fmt.Errorf("delete sandbox claim %s: %w", req.SandboxName, err)
	}
	d.sessionMu.Lock()
	delete(d.sessions, req.SandboxName)
	d.sessionMu.Unlock()
	req.Logf("sandbox %s destroyed", req.SandboxName)
	return engine.TurnResponse{Output: json.RawMessage(`{"destroyed":true}`)}, nil
}

func (d *Driver) run(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	timings, phase := newTimings()
	pod, err := d.resolveSession(ctx, req.SandboxName)
	if err != nil {
		return engine.TurnResponse{}, err
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
		res, err := d.exec(ctx, pod.Name, []string{"sh", "-c", script}, nil)
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
		transcript := engine.Workspace + "/" + engine.ConversationFile
		var history []engine.ConversationTurn
		if prev, err := d.exec(ctx, pod.Name, []string{"cat", transcript}, nil); err == nil && prev.ExitCode == 0 {
			history = engine.ParseConversation(prev.Stdout)
		}
		res, err := d.exec(ctx, pod.Name, engine.PromptCommand(engine.Workspace, in.Prompt, history), nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		if res.ExitCode != 0 {
			return engine.TurnResponse{}, fmt.Errorf("claude failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr+string(res.Stdout)))
		}
		if apiMs := engine.ParsePromptAPIMs(string(res.Stdout)); apiMs > 0 {
			timings["llm"] = apiMs
		}
		reply := engine.ParsePromptReply(string(res.Stdout))
		if _, err := d.mustExec(ctx, pod.Name, []string{"sh", "-c", "cat > " + transcript}, engine.AppendTurn(history, in.Prompt, reply)); err != nil {
			req.Logf("conversation append failed: %v", err)
		}
		ls, err := d.mustExec(ctx, pod.Name, []string{"ls", "-1", engine.Workspace}, nil)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = engine.PromptOutput{Reply: reply, Files: engine.FilterFiles(strings.Split(strings.TrimSpace(string(ls.Stdout)), "\n"))}
	default:
		return engine.TurnResponse{}, fmt.Errorf("unknown sandbox fn %q", req.Fn)
	}

	phase("exec", execStart)
	req.Logf("fn %s executed in %s", req.Fn, time.Since(execStart).Round(time.Millisecond))
	outJSON, err := json.Marshal(output)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	checkpointStart := time.Now()
	tar, err := d.mustExec(ctx, pod.Name, []string{"tar", "-czf", "-", "-C", engine.Workspace, "."}, nil)
	if err != nil {
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace: %w", err)
	}
	phase("checkpoint", checkpointStart)
	return engine.TurnResponse{Output: outJSON, SnapshotTar: tar.Stdout, Timings: timings}, nil
}
