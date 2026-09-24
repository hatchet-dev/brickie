package awsboot

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KataRuntimeClass is the runtime class kata-deploy creates for Cloud Hypervisor.
const (
	KataRuntimeClass = "kata-clh"
	kataChart        = "oci://ghcr.io/kata-containers/kata-deploy-charts/kata-deploy"
	kataChartVersion = "4.2.0"
	kataNodeLabel    = "katacontainers.io/kata-runtime"
)

// EnsureKata installs kata-deploy with only the Cloud Hypervisor shim. The
// nodes need KVM, so this is only valid on bare-metal amd64 instances.
func EnsureKata(ctx context.Context, cfg *rest.Config) error {
	values := map[string]any{
		"k8sDistribution": "k8s",
		"deploymentMode":  "daemonset",
		"shims": map[string]any{
			"disableAll": true,
			"clh":        map[string]any{"enabled": true},
		},
		"defaultShim":    map[string]any{"amd64": "clh"},
		"runtimeClasses": map[string]any{"enabled": true},
	}
	return InstallChart(ctx, cfg, "kube-system", "kata-deploy", kataChart, kataChartVersion, values)
}

// WaitKataReady blocks until every node is labelled by kata-deploy and the runtime class exists.
func WaitKataReady(ctx context.Context, clientset *kubernetes.Clientset, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err == nil && len(nodes.Items) > 0 {
			ready := 0
			for _, n := range nodes.Items {
				if n.Labels[kataNodeLabel] == "true" {
					ready++
				}
			}
			if ready == len(nodes.Items) {
				if _, err := clientset.NodeV1().RuntimeClasses().Get(ctx, KataRuntimeClass, metav1.GetOptions{}); err == nil {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("kata runtime not ready on all nodes within %s", timeout)
}
