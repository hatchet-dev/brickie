// Package kubernetes runs sandboxes as agent-sandbox claims: one warm pool per
// image, one claim and pod per sandbox, sandboxd reached over the pod IP
// through the mutual-TLS proxy that runs beside it.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extensionsv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/hatchet-dev/brickie/internal/driver"
	"github.com/hatchet-dev/brickie/internal/driver/fsops"
	"github.com/hatchet-dev/brickie/internal/engine"
	"github.com/hatchet-dev/brickie/internal/sandboxd"
	"github.com/hatchet-dev/brickie/internal/sandboxtls"
)

const (
	sandboxLabel       = "brickie.hatchet.run/sandbox"
	lastUsedAnnotation = "brickie.hatchet.run/last-used"
	userLabelPrefix    = "sandbox.users.io/"
	markerFile         = fsops.Marker
	tokenSecret        = "brickie-sandboxd"
	tlsSecret          = "brickie-sandbox-tls"
	tlsDir             = "/etc/brickie/tls"
	gvisorRuntimeClass = "gvisor"
	kataRuntimeClass   = "kata-clh"
	nitroRuntimeClass  = "isolated-sandbox"
	readyTimeout       = 5 * time.Minute
)

var runtimeClasses = map[string]string{"gvisor": gvisorRuntimeClass, "kata": kataRuntimeClass, "nitro": nitroRuntimeClass}

var autoRuntimeClasses = []string{kataRuntimeClass, gvisorRuntimeClass, nitroRuntimeClass}

// Options configures the driver; zero values take the defaults.
type Options struct {
	Namespace    string
	Image        string
	Runtime      string
	WarmReplicas int
	IdleTTL      time.Duration
	MaxFileBytes int64
	Staging      driver.Staging

	// ProviderNamespace holds the sandboxd token and CA Secret and is where
	// the provider pod, matched by ProviderSelector, is allowed to reach
	// sandboxes. ProviderImage is this provider's own image, which runs in
	// every sandbox pod as the TLS proxy in front of sandboxd.
	ProviderNamespace string
	ProviderSelector  map[string]string
	ProviderImage     string
	// Egress is what sandboxes may reach: "internet" (public addresses and
	// cluster DNS, never private ranges or the metadata endpoint) or "none".
	Egress string
	// CPU, Memory, and Disk bound each sandbox; Disk also caps the workspace.
	CPU, Memory, Disk resource.Quantity
	// NodeSelector and Tolerations place sandbox pods, on top of whatever the
	// runtime class adds.
	NodeSelector   map[string]string
	Tolerations    []corev1.Toleration
	InitContainers []corev1.Container
}

type Driver struct {
	opts         Options
	client       ctrlclient.Client
	runtimeClass string
	token        string
	ca           *sandboxtls.CA
	tls          *tls.Config

	poolMu sync.Mutex
	pools  map[string]string

	sandboxMu sync.Mutex
	sandboxes map[string]*sync.Mutex
}

// New connects to the cluster, selects the runtime class, and warms the pool
// for the default image.
func New(opts Options) (*Driver, error) {
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}
	if opts.Image == "" {
		opts.Image = "alpine:3"
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 30 * time.Minute
	}
	if opts.WarmReplicas <= 0 {
		opts.WarmReplicas = 2
	}
	if opts.ProviderNamespace == "" {
		if ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			opts.ProviderNamespace = strings.TrimSpace(string(ns))
		} else {
			opts.ProviderNamespace = opts.Namespace
		}
	}
	if len(opts.ProviderSelector) == 0 {
		opts.ProviderSelector = map[string]string{"app.kubernetes.io/name": "brickie"}
	}
	if opts.ProviderImage == "" {
		return nil, fmt.Errorf("the kubernetes driver needs --provider-image: the image this provider runs from, for the TLS proxy in every sandbox pod")
	}
	switch opts.Egress {
	case "":
		opts.Egress = "internet"
	case "internet", "none":
	default:
		return nil, fmt.Errorf("unknown sandbox egress %q (internet|none)", opts.Egress)
	}
	for q, def := range map[*resource.Quantity]string{&opts.CPU: "1", &opts.Memory: "2Gi", &opts.Disk: "10Gi"} {
		if q.IsZero() {
			*q = resource.MustParse(def)
		}
	}
	restCfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	sch := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{scheme.AddToScheme, sandboxv1beta1.AddToScheme, extensionsv1beta1.AddToScheme} {
		if err := add(sch); err != nil {
			return nil, err
		}
	}
	c, err := ctrlclient.New(restCfg, ctrlclient.Options{Scheme: sch})
	if err != nil {
		return nil, err
	}
	d := &Driver{opts: opts, client: c, pools: map[string]string{}, sandboxes: map[string]*sync.Mutex{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.detectRuntime(ctx); err != nil {
		return nil, err
	}
	if err := d.ensureCredentials(ctx); err != nil {
		return nil, err
	}
	if err := d.ensureServerCert(ctx); err != nil {
		return nil, err
	}
	if _, err := d.ensurePool(ctx, opts.Image); err != nil {
		return nil, err
	}
	go func() {
		for range time.Tick(time.Hour) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := d.ensureServerCert(ctx); err != nil {
				slog.Error("renew sandbox tls secret", "err", err)
			}
			cancel()
		}
	}()
	return d, nil
}

func loadConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
}

func (d *Driver) detectRuntime(ctx context.Context) error {
	has := func(name string) bool {
		return d.client.Get(ctx, ctrlclient.ObjectKey{Name: name}, &nodev1.RuntimeClass{}) == nil
	}
	switch mode := d.opts.Runtime; mode {
	case "none":
		return nil
	case "", "auto":
		for _, rc := range autoRuntimeClasses {
			if has(rc) {
				d.runtimeClass = rc
				break
			}
		}
		if d.runtimeClass == "" {
			slog.Warn("no sandbox runtime class found; sandboxes run with the default runtime", "tried", strings.Join(autoRuntimeClasses, ", "))
			return nil
		}
	default:
		rc, ok := runtimeClasses[mode]
		if !ok {
			rc = mode
		}
		if !has(rc) {
			return fmt.Errorf("runtime %s requested but the %q runtime class is not on this cluster", mode, rc)
		}
		d.runtimeClass = rc
	}
	slog.Info("sandboxes run under runtime class", "runtimeClass", d.runtimeClass)
	return nil
}

// ensureCredentials reads the Secret in the provider's own namespace that
// holds the sandboxd bearer token and the CA behind every sandbox's TLS,
// creating whichever is missing. Sandbox pods only ever see the token's
// digest and the CA certificate.
func (d *Driver) ensureCredentials(ctx context.Context) error {
	ns := d.opts.ProviderNamespace
	s := &corev1.Secret{Name: tokenSecret, Namespace: ns}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := d.client.Get(ctx, ctrlclient.ObjectKeyFromObject(s), s)
		if apierrors.IsNotFound(err) {
			*s = corev1.Secret{Name: tokenSecret, Namespace: ns}
			if err = d.client.Create(ctx, s); apierrors.IsAlreadyExists(err) {
				err = d.client.Get(ctx, ctrlclient.ObjectKeyFromObject(s), s)
			}
		}
		if err != nil {
			return err
		}
		if s.Data == nil {
			s.Data = map[string][]byte{}
		}
		changed := false
		if len(s.Data["token"]) == 0 {
			token, err := sandboxd.NewToken()
			if err != nil {
				return err
			}
			s.Data["token"] = []byte(token)
			changed = true
		}
		if len(s.Data["ca.crt"]) == 0 || len(s.Data["ca.key"]) == 0 {
			ca, err := sandboxtls.NewCA()
			if err != nil {
				return err
			}
			s.Data["ca.crt"], s.Data["ca.key"] = ca.CertPEM, ca.KeyPEM
			changed = true
		}
		if !changed {
			return nil
		}
		return d.client.Update(ctx, s)
	})
	if err != nil {
		return fmt.Errorf("sandboxd secret %s/%s: %w", ns, tokenSecret, err)
	}
	d.token = string(s.Data["token"])
	if d.ca, err = sandboxtls.LoadCA(s.Data["ca.crt"], s.Data["ca.key"]); err != nil {
		return fmt.Errorf("sandboxd secret %s/%s: %w", ns, tokenSecret, err)
	}
	d.tls = d.ca.ClientConfig()
	return nil
}

// ensureServerCert keeps the Secret the proxies mount in the sandbox
// namespace: the CA certificate and a server certificate, reissued a week
// before it expires or when the CA changed.
func (d *Driver) ensureServerCert(ctx context.Context) error {
	ns := d.opts.Namespace
	s := &corev1.Secret{Name: tlsSecret, Namespace: ns}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := d.client.Get(ctx, ctrlclient.ObjectKeyFromObject(s), s)
		if err == nil && bytes.Equal(s.Data[sandboxtls.CAFile], d.ca.CertPEM) && d.ca.Fresh(s.Data[sandboxtls.CertFile]) {
			return nil
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		certPEM, keyPEM, issueErr := d.ca.Issue(x509.ExtKeyUsageServerAuth)
		if issueErr != nil {
			return issueErr
		}
		data := map[string][]byte{sandboxtls.CAFile: d.ca.CertPEM, sandboxtls.CertFile: certPEM, sandboxtls.KeyFile: keyPEM}
		if apierrors.IsNotFound(err) {
			*s = corev1.Secret{Name: tlsSecret, Namespace: ns, Data: data}
			return d.client.Create(ctx, s)
		}
		s.Data = data
		return d.client.Update(ctx, s)
	})
	if err != nil {
		return fmt.Errorf("sandbox tls secret %s/%s: %w", ns, tlsSecret, err)
	}
	return nil
}

// networkPolicy is the template's managed policy: the sandbox's TLS ports
// reachable only from the provider pod, and egress by the Egress option.
func (d *Driver) networkPolicy() *extensionsv1beta1.NetworkPolicySpec {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	rest, grpc, dns := intstr.FromInt32(sandboxtls.RESTPort), intstr.FromInt32(sandboxtls.GRPCPort), intstr.FromInt32(53)
	np := &extensionsv1beta1.NetworkPolicySpec{
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": d.opts.ProviderNamespace}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: d.opts.ProviderSelector},
			}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &rest}, {Protocol: &tcp, Port: &grpc}},
		}},
	}
	if d.opts.Egress == "internet" {
		np.Egress = []networkingv1.NetworkPolicyEgressRule{
			{
				To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns}},
			},
			{
				To: []networkingv1.NetworkPolicyPeer{
					{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"}}},
					{IPBlock: &networkingv1.IPBlock{CIDR: "::/0", Except: []string{"fc00::/7", "fe80::/10"}}},
				},
			},
		}
	}
	return np
}

// podSpec is the sandbox pod: sandboxd on loopback only in the sandbox
// container, and the provider's own image beside it terminating mutual TLS on
// the pod IP. The certificate Secret is mounted into the proxy alone.
func (d *Driver) podSpec(image string) corev1.PodSpec {
	no, yes := false, true
	disk := d.opts.Disk
	spec := corev1.PodSpec{
		AutomountServiceAccountToken: &no,
		SecurityContext:              &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		InitContainers: append(append([]corev1.Container{}, d.opts.InitContainers...), corev1.Container{
			Name:         "sandboxd",
			Image:        sandboxd.Image,
			Command:      []string{"/bin/sh", "-c", "cp " + sandboxd.ImageBinary + " /brickie/sandboxd"},
			VolumeMounts: []corev1.VolumeMount{{Name: "brickie", MountPath: "/brickie"}},
		}),
		Containers: []corev1.Container{
			{
				Name:            "sandbox",
				Image:           image,
				Command:         append([]string{"/brickie/sandboxd"}, sandboxd.Args(d.token, "127.0.0.1")...),
				WorkingDir:      sandboxd.Workspace,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceEphemeralStorage: resource.MustParse("50Mi")},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:              d.opts.CPU,
						corev1.ResourceMemory:           d.opts.Memory,
						corev1.ResourceEphemeralStorage: d.opts.Disk,
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "brickie", MountPath: "/brickie", ReadOnly: true},
					{Name: "workspace", MountPath: sandboxd.Workspace},
				},
			},
			{
				Name:  "tls",
				Image: d.opts.ProviderImage,
				Args:  []string{"sandbox-tls", "--dir", tlsDir},
				Ports: []corev1.ContainerPort{
					{Name: "rest", ContainerPort: sandboxtls.RESTPort},
					{Name: "grpc", ContainerPort: sandboxtls.GRPCPort},
					{Name: "health", ContainerPort: sandboxtls.HealthPort},
				},
				ReadinessProbe: &corev1.Probe{
					HTTPGet:       &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(sandboxtls.HealthPort)},
					PeriodSeconds: 2,
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no,
					ReadOnlyRootFilesystem:   &yes,
					RunAsNonRoot:             &yes,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				}},
				VolumeMounts: []corev1.VolumeMount{{Name: "tls", MountPath: tlsDir, ReadOnly: true}},
			},
		},
		Volumes: []corev1.Volume{
			{Name: "brickie", EmptyDir: &corev1.EmptyDirVolumeSource{}},
			{Name: "workspace", EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &disk}},
			{Name: "tls", Secret: &corev1.SecretVolumeSource{SecretName: tlsSecret}},
		},
	}
	if d.runtimeClass != "" {
		rc := d.runtimeClass
		spec.RuntimeClassName = &rc
	}
	spec.NodeSelector = d.opts.NodeSelector
	spec.Tolerations = d.opts.Tolerations
	return spec
}

func poolName(image string) string {
	return "brickie-warm-" + fmt.Sprintf("%x", sha256.Sum256([]byte(image)))[:16]
}

// ensurePool upserts the SandboxTemplate and SandboxWarmPool for an image the
// first time it is seen.
func (d *Driver) ensurePool(ctx context.Context, image string) (string, error) {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	if name, ok := d.pools[image]; ok {
		return name, nil
	}
	name := poolName(image)
	tmpl := &extensionsv1beta1.SandboxTemplate{
		Name: name, Namespace: d.opts.Namespace,
		Spec: extensionsv1beta1.SandboxTemplateSpec{
			SandboxBlueprint:        sandboxv1beta1.SandboxBlueprint{PodTemplate: sandboxv1beta1.PodTemplate{Spec: d.podSpec(image)}},
			NetworkPolicyManagement: extensionsv1beta1.NetworkPolicyManagementManaged,
			NetworkPolicy:           d.networkPolicy(),
		},
	}
	replicas := int32(min(d.opts.WarmReplicas, 1000)) // #nosec G115 -- bounded above
	pool := &extensionsv1beta1.SandboxWarmPool{
		Name: name, Namespace: d.opts.Namespace,
		Spec: extensionsv1beta1.SandboxWarmPoolSpec{
			Replicas:       &replicas,
			TemplateRef:    extensionsv1beta1.SandboxTemplateRef{Name: name},
			UpdateStrategy: &extensionsv1beta1.SandboxWarmPoolUpdateStrategy{Type: extensionsv1beta1.RecreateSandboxWarmPoolUpdateStrategyType},
		},
	}
	if err := d.upsert(ctx, tmpl, func(existing ctrlclient.Object) {
		existing.(*extensionsv1beta1.SandboxTemplate).Spec = tmpl.Spec
	}); err != nil {
		return "", fmt.Errorf("sandbox template for %s: %w", image, err)
	}
	if err := d.upsert(ctx, pool, func(existing ctrlclient.Object) {
		existing.(*extensionsv1beta1.SandboxWarmPool).Spec = pool.Spec
	}); err != nil {
		return "", fmt.Errorf("warm pool for %s: %w", image, err)
	}
	d.pools[image] = name
	slog.Info("warm pool ready", "pool", name, "image", image, "replicas", replicas)
	return name, nil
}

func (d *Driver) upsert(ctx context.Context, obj ctrlclient.Object, update func(existing ctrlclient.Object)) error {
	err := d.client.Create(ctx, obj)
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := obj.DeepCopyObject().(ctrlclient.Object)
		if err := d.client.Get(ctx, ctrlclient.ObjectKeyFromObject(obj), existing); err != nil {
			return err
		}
		update(existing)
		return d.client.Update(ctx, existing)
	})
}

func userLabels(metadata map[string]string) map[string]string {
	labels := map[string]string{}
	for k, v := range metadata {
		if len(validation.IsQualifiedName(userLabelPrefix+k)) == 0 && len(validation.IsValidLabelValue(v)) == 0 {
			labels[userLabelPrefix+k] = v
		}
	}
	return labels
}

func ready(c *extensionsv1beta1.SandboxClaim) bool {
	return c.DeletionTimestamp == nil && c.Status.SandboxStatus.Name != "" && len(c.Status.SandboxStatus.PodIPs) > 0 && meta.IsStatusConditionTrue(c.Status.Conditions, "Ready")
}

func (d *Driver) getClaim(ctx context.Context, name string) (*extensionsv1beta1.SandboxClaim, error) {
	var claim extensionsv1beta1.SandboxClaim
	if err := d.client.Get(ctx, ctrlclient.ObjectKey{Namespace: d.opts.Namespace, Name: name}, &claim); err != nil {
		return nil, err
	}
	return &claim, nil
}

// ensureClaim binds a warm pod to the sandbox, or finds the pod it already
// has, and waits until it is ready.
func (d *Driver) ensureClaim(ctx context.Context, pool string, req engine.TurnRequest, ttl time.Duration) (*extensionsv1beta1.SandboxClaim, error) {
	newClaim := func() *extensionsv1beta1.SandboxClaim {
		return &extensionsv1beta1.SandboxClaim{
			Name: req.SandboxName, Namespace: d.opts.Namespace, Labels: map[string]string{sandboxLabel: req.SandboxID},
			Spec: extensionsv1beta1.SandboxClaimSpec{
				WarmPoolRef:           extensionsv1beta1.SandboxWarmPoolRef{Name: pool},
				Lifecycle:             &extensionsv1beta1.Lifecycle{ShutdownTime: shutdownTime(ttl), ShutdownPolicy: extensionsv1beta1.ShutdownPolicyDelete},
				AdditionalPodMetadata: sandboxv1beta1.PodMetadata{Labels: userLabels(req.Metadata)},
			},
		}
	}
	existing, err := d.getClaim(ctx, req.SandboxName)
	switch {
	case err == nil && existing.DeletionTimestamp == nil:
	case err == nil:
		if err := d.waitGone(ctx, req.SandboxName); err != nil {
			return nil, err
		}
		fallthrough
	case apierrors.IsNotFound(err):
		if err := d.client.Create(ctx, newClaim()); err != nil {
			return nil, fmt.Errorf("create sandbox claim %s: %w", req.SandboxName, err)
		}
	default:
		return nil, err
	}
	deadline := time.Now().Add(readyTimeout)
	var last *extensionsv1beta1.SandboxClaim
	for time.Now().Before(deadline) {
		if claim, err := d.getClaim(ctx, req.SandboxName); err == nil {
			last = claim
			if ready(claim) {
				return claim, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	detail := "the claim never came back from the api server"
	if last != nil {
		if c := meta.FindStatusCondition(last.Status.Conditions, "Ready"); c != nil {
			detail = c.Reason + ": " + c.Message
		}
	}
	return nil, fmt.Errorf("sandbox claim %s not ready within %s (%s)", req.SandboxName, readyTimeout, detail)
}

func (d *Driver) waitGone(ctx context.Context, name string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := d.getClaim(ctx, name); apierrors.IsNotFound(err) {
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

func shutdownTime(ttl time.Duration) *metav1.Time {
	t := metav1.NewTime(time.Now().Add(ttl))
	return &t
}

func (d *Driver) touch(ctx context.Context, name string, ttl time.Duration) error {
	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{lastUsedAnnotation: time.Now().UTC().Format(time.RFC3339)}},
		"spec":     map[string]any{"lifecycle": map[string]any{"shutdownTime": shutdownTime(ttl)}},
	})
	claim := &extensionsv1beta1.SandboxClaim{Name: name, Namespace: d.opts.Namespace}
	return d.client.Patch(ctx, claim, ctrlclient.RawPatch(types.MergePatchType, body))
}

func (d *Driver) dial(ctx context.Context, claim *extensionsv1beta1.SandboxClaim) (*sandboxd.Client, error) {
	ip := claim.Status.SandboxStatus.PodIPs[0]
	return sandboxd.Dial(ctx, net.JoinHostPort(ip, strconv.Itoa(sandboxtls.GRPCPort)), net.JoinHostPort(ip, strconv.Itoa(sandboxtls.RESTPort)), d.token, d.tls)
}

// hold renews the claim's deadline for as long as the turn runs, so no turn,
// however long, loses its pod to idle expiry. The idle window starts again
// when the caller touches the claim after the turn.
func (d *Driver) hold(ctx context.Context, req engine.TurnRequest, ttl time.Duration) (release func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(max(ttl/2, time.Second))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := d.touch(ctx, req.SandboxName, ttl); err != nil && ctx.Err() == nil {
					req.Logf("renew sandbox %s deadline failed: %v", req.SandboxName, err)
				}
			}
		}
	}()
	return cancel
}

func (d *Driver) lock(name string) *sync.Mutex {
	d.sandboxMu.Lock()
	defer d.sandboxMu.Unlock()
	mu, ok := d.sandboxes[name]
	if !ok {
		mu = &sync.Mutex{}
		d.sandboxes[name] = mu
	}
	return mu
}

func (d *Driver) Live(ctx context.Context) map[string]time.Time {
	var claims extensionsv1beta1.SandboxClaimList
	if err := d.client.List(ctx, &claims, ctrlclient.InNamespace(d.opts.Namespace), ctrlclient.HasLabels{sandboxLabel}); err != nil {
		return nil
	}
	out := map[string]time.Time{}
	for i := range claims.Items {
		c := &claims.Items[i]
		if !ready(c) {
			continue
		}
		t, err := time.Parse(time.RFC3339, c.Annotations[lastUsedAnnotation])
		if err != nil {
			t = c.CreationTimestamp.Time
		}
		out[c.Name] = t
	}
	return out
}

// RunTurn runs one turn; turns on the same sandbox are serialized.
func (d *Driver) RunTurn(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	mu := d.lock(req.SandboxName)
	mu.Lock()
	defer mu.Unlock()
	if req.Fn == engine.DestroyFn {
		claim := &extensionsv1beta1.SandboxClaim{Name: req.SandboxName, Namespace: d.opts.Namespace}
		if err := d.client.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
			return engine.TurnResponse{}, fmt.Errorf("delete sandbox claim %s: %w", req.SandboxName, err)
		}
		req.Logf("sandbox %s destroyed", req.SandboxName)
		return engine.TurnResponse{Output: json.RawMessage(`{"destroyed":true}`)}, nil
	}
	ttl := req.IdleTTL
	if ttl <= 0 {
		ttl = d.opts.IdleTTL
	}
	timings := map[string]int64{}
	start := time.Now()
	var claim *extensionsv1beta1.SandboxClaim
	var err error
	starting := req.Fn == engine.CreateFn || req.Fn == engine.ConnectFn
	if starting {
		pool, err := d.ensurePool(ctx, req.Image)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		if claim, err = d.ensureClaim(ctx, pool, req, ttl); err != nil {
			return engine.TurnResponse{}, err
		}
	} else if claim, err = d.getClaim(ctx, req.SandboxName); err != nil || !ready(claim) {
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s is not running (idle-expired or killed): connect to it first", req.SandboxID)
	}
	c, err := d.dial(ctx, claim)
	if err != nil {
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s: %w", req.SandboxName, err)
	}
	defer c.Close()
	_, err = c.Stat(ctx, markerFile)
	fresh := sandboxd.IsNotFound(err)
	switch {
	case err != nil && !fresh:
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s: %w", req.SandboxName, err)
	case fresh && !starting:
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s was replaced (its pod restarted) and its live workspace is lost: connect to it again to restore the latest snapshot", req.SandboxID)
	case fresh:
		req.Logf("sandbox %s bound to pod %s from pool %s", req.SandboxName, claim.Status.SandboxStatus.Name, claim.Spec.WarmPoolRef.Name)
	default:
		req.Logf("sandbox %s reused (pod %s)", req.SandboxName, claim.Status.SandboxStatus.Name)
	}
	driver.Phase(timings, "claim", start)
	if err := d.touch(ctx, req.SandboxName, ttl); err != nil {
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s: hold claim: %w", req.SandboxName, err)
	}
	release := d.hold(ctx, req, ttl)
	resp, err := driver.Run(ctx, c, req, fresh, timings, driver.Options{MaxFileBytes: d.opts.MaxFileBytes, Staging: d.opts.Staging})
	release()
	if err != nil {
		return engine.TurnResponse{}, err
	}
	if fresh {
		if err := c.Write(ctx, markerFile, strings.NewReader(req.SandboxID), int64(len(req.SandboxID))); err != nil {
			return engine.TurnResponse{}, fmt.Errorf("sandbox %s: %w", req.SandboxName, err)
		}
	}
	if err := d.touch(ctx, req.SandboxName, ttl); err != nil {
		req.Logf("extend sandbox %s deadline failed: %v", req.SandboxName, err)
	}
	return resp, nil
}
