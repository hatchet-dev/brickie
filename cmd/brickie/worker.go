package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/spf13/cobra"

	"github.com/hatchet-dev/brickie/internal/driver/kubernetes"
	"github.com/hatchet-dev/brickie/internal/driver/local"
	"github.com/hatchet-dev/brickie/internal/engine"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func workerCmd() *cobra.Command {
	var (
		driverName  string
		namespace   string
		image       string
		credentials string
		snapshotDir string
		turnTimeout time.Duration
		kubeContext string
		slots       int
	)
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the sandbox provider as a Hatchet worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			var driver engine.Driver
			switch driverName {
			case "local":
				driver = local.New()
			case "kubernetes":
				creds := []string{}
				for _, c := range strings.Split(credentials, ",") {
					if c = strings.TrimSpace(c); c != "" {
						creds = append(creds, c)
					}
				}
				d, err := kubernetes.New(kubernetes.Options{
					Context:     kubeContext,
					Namespace:   namespace,
					Image:       image,
					Credentials: creds,
				})
				if err != nil {
					return err
				}
				driver = d
			default:
				return fmt.Errorf("unknown driver %q (local|kubernetes)", driverName)
			}

			client, err := hatchet.NewClient()
			if err != nil {
				return err
			}
			task := engine.NewProviderTask(client, engine.ProviderOpts{
				Driver:      driver,
				Store:       engine.FSSnapshotStore{Dir: snapshotDir},
				TurnTimeout: turnTimeout,
			})
			w, err := client.NewWorker("hatchet-sandbox-provider", hatchet.WithWorkflows(task), hatchet.WithDurableSlots(slots))
			if err != nil {
				return err
			}
			ctx, cancel := cmdutils.NewInterruptContext()
			defer cancel()
			fmt.Fprintf(os.Stderr, "brickie: sandbox provider started (driver=%s)\n", driverName)
			return w.StartBlocking(ctx)
		},
	}
	cmd.Flags().StringVar(&driverName, "driver", envOr("BRICKIE_DRIVER", "local"), "sandbox driver: local (Docker Sandboxes) or kubernetes")
	cmd.Flags().StringVar(&namespace, "namespace", envOr("BRICKIE_SANDBOX_NAMESPACE", "default"), "kubernetes namespace for sandboxes")
	cmd.Flags().StringVar(&image, "image", envOr("BRICKIE_SANDBOX_IMAGE", ""), "default sandbox image")
	cmd.Flags().StringVar(&credentials, "credentials", envOr("BRICKIE_CREDENTIALS", ""), "comma-separated env var names to inject into sandboxes")
	cmd.Flags().StringVar(&snapshotDir, "snapshot-dir", envOr("BRICKIE_SNAPSHOT_DIR", engine.DefaultSnapshotDir()), "snapshot storage directory")
	defaultTimeout := 15 * time.Minute
	if v := os.Getenv("BRICKIE_TURN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			defaultTimeout = d
		}
	}
	cmd.Flags().DurationVar(&turnTimeout, "turn-timeout", defaultTimeout, "execution timeout per sandbox turn")
	cmd.Flags().StringVar(&kubeContext, "context", envOr("BRICKIE_KUBE_CONTEXT", ""), "kubeconfig context (kubernetes driver)")
	cmd.Flags().IntVar(&slots, "slots", 10, "concurrent sandbox turns")
	return cmd
}
