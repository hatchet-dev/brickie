package awsboot

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const gvisorRelease = "20260907.0"

const gvisorInstallScript = `set -e
apk add --no-cache util-linux >/dev/null
CONFIG=/host/etc/containerd/config.toml

install_binaries() {
  [ -x /host/usr/local/bin/runsc ] && [ -x /host/usr/local/bin/containerd-shim-runsc-v1 ] && return 0
  ARCH=$(uname -m)
  BASE=https://storage.googleapis.com/gvisor/releases/release/RELEASE/$ARCH
  cd /tmp
  for f in runsc containerd-shim-runsc-v1; do
    if ! wget -q $BASE/$f -O $f || ! wget -q $BASE/$f.sha512 -O $f.sha512; then
      BASE=https://storage.googleapis.com/gvisor/releases/release/latest/$ARCH
      wget -q $BASE/$f -O $f
      wget -q $BASE/$f.sha512 -O $f.sha512
    fi
    sha512sum -c $f.sha512
    chmod +x $f
    mv $f /host/usr/local/bin/$f
    rm -f $f.sha512
  done
}

effective() {
  chroot /host containerd config dump 2>/dev/null | grep -q 'runtimes.runsc'
}

apply_config() {
  if grep -Eq '^version *= *3' $CONFIG 2>/dev/null; then
    TABLE='io.containerd.cri.v1.runtime'
  elif grep -Eq '^version *= *2' $CONFIG 2>/dev/null; then
    TABLE='io.containerd.grpc.v1.cri'
  elif chroot /host containerd --version 2>/dev/null | grep -Eq 'containerd/v2|containerd v2| v2\.'; then
    TABLE='io.containerd.cri.v1.runtime'
  else
    TABLE='io.containerd.grpc.v1.cri'
  fi
  if ! grep -q 'runtimes.runsc' $CONFIG 2>/dev/null; then
    printf '\n[plugins."%s".containerd.runtimes.runsc]\n  runtime_type = "io.containerd.runsc.v1"\n' "$TABLE" >> $CONFIG
  fi
  nsenter -t 1 -m -- systemctl restart containerd
}

install_binaries
while true; do
  if effective; then
    touch /tmp/gvisor-ready
  else
    rm -f /tmp/gvisor-ready
    echo "runsc missing from effective containerd config; applying"
    apply_config
  fi
  sleep 30
done`

func EnsureGVisor(ctx context.Context, clientset *kubernetes.Clientset) error {
	handler := "runsc"
	rc := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "gvisor"},
		Handler:    handler,
	}
	if _, err := clientset.NodeV1().RuntimeClasses().Create(ctx, rc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create gvisor runtime class: %w", err)
	}

	privileged := true
	hostPathDir := corev1.HostPathDirectory
	labels := map[string]string{"app.kubernetes.io/name": "gvisor-installer"}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "gvisor-installer", Namespace: "kube-system", Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					HostPID:     true,
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					Containers: []corev1.Container{{
						Name:            "installer",
						Image:           "alpine:3.22",
						Command:         []string{"/bin/sh", "-c", strings.ReplaceAll(gvisorInstallScript, "RELEASE", gvisorRelease)},
						SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
						VolumeMounts:    []corev1.VolumeMount{{Name: "host", MountPath: "/host"}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{Command: []string{"test", "-f", "/tmp/gvisor-ready"}},
							},
							PeriodSeconds: 5,
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "host",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/", Type: &hostPathDir},
						},
					}},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().DaemonSets("kube-system").Create(ctx, ds, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create gvisor installer daemonset: %w", err)
		}
		if _, err := clientset.AppsV1().DaemonSets("kube-system").Update(ctx, ds, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update gvisor installer daemonset: %w", err)
		}
	}
	return nil
}

func WaitGVisorReady(ctx context.Context, clientset *kubernetes.Clientset, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ds, err := clientset.AppsV1().DaemonSets("kube-system").Get(ctx, "gvisor-installer", metav1.GetOptions{})
		if err == nil && ds.Status.DesiredNumberScheduled > 0 && ds.Status.NumberReady == ds.Status.DesiredNumberScheduled {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("gvisor installer daemonset not ready within %s", timeout)
}
