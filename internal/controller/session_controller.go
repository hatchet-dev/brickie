package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"

	brickiev1alpha1 "github.com/hatchet-dev/brickie/api/v1alpha1"
)

type SandboxSessionReconciler struct {
	ctrlclient.Client
}

func (r *SandboxSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var session brickiev1alpha1.SandboxSession
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		return ctrl.Result{}, ctrlclient.IgnoreNotFound(err)
	}

	// TODO: ensure an agent-sandbox Sandbox exists for this session, mounting the
	// brickie CSI volume (driver csi.brickie.hatchet.run) seeded from spec.fromSnapshot.
	// TODO: reconcile TTL into deletion and record status.lastSnapshotId when the
	// CSI node captures a WorkspaceSnapshot on unmount.

	if session.Status.Phase == "" {
		session.Status.Phase = brickiev1alpha1.SessionPending
		if err := r.Status().Update(ctx, &session); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	logger.Info("reconciled sandbox session", "session", req.NamespacedName, "phase", session.Status.Phase)
	return ctrl.Result{}, nil
}

func (r *SandboxSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&brickiev1alpha1.SandboxSession{}).
		Owns(&sandboxv1beta1.Sandbox{}).
		Complete(r)
}
