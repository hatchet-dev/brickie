package main

import (
	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	brickiev1alpha1 "github.com/hatchet-dev/brickie/api/v1alpha1"
	"github.com/hatchet-dev/brickie/internal/controller"
)

func controllerCmd() *cobra.Command {
	var metricsAddr string
	cmd := &cobra.Command{
		Use:   "controller",
		Short: "Run the brickie Kubernetes controller and the Hatchet sandbox provider in one process",
		RunE: func(cmd *cobra.Command, args []string) error {
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				return err
			}
			if err := sandboxv1beta1.AddToScheme(scheme); err != nil {
				return err
			}
			if err := brickiev1alpha1.AddToScheme(scheme); err != nil {
				return err
			}

			mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
				Scheme:  scheme,
				Metrics: ctrlmetrics.Options{BindAddress: metricsAddr},
			})
			if err != nil {
				return err
			}
			reconciler := &controller.SandboxSessionReconciler{Client: mgr.GetClient()}
			if err := reconciler.SetupWithManager(mgr); err != nil {
				return err
			}

			ctx, cancel := cmdutils.NewInterruptContext()
			defer cancel()

			g, gctx := errgroup.WithContext(ctx)
			g.Go(func() error { return mgr.Start(gctx) })
			g.Go(func() error { return runProvider(gctx, providerCfg) })
			return g.Wait()
		},
	}
	providerCfg.registerFlags(cmd)
	_ = cmd.Flags().Set("driver", "kubernetes")
	cmd.Flags().Lookup("driver").DefValue = "kubernetes"
	cmd.Flags().StringVar(&metricsAddr, "metrics-bind-address", ":8082", "controller metrics bind address (0 to disable)")
	return cmd
}
