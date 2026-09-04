package main

import (
	"context"
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

type providerConfig struct {
	driver      string
	namespace   string
	image       string
	credentials string
	snapshotDir string
	kubeContext string
	turnTimeout time.Duration
	slots       int
}

func (c providerConfig) registerFlags(cmd *cobra.Command) {
	defaultTimeout := 15 * time.Minute
	if v := os.Getenv("BRICKIE_TURN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			defaultTimeout = d
		}
	}
	cmd.Flags().StringVar(&providerCfg.driver, "driver", envOr("BRICKIE_DRIVER", "local"), "sandbox driver: local (Docker Sandboxes) or kubernetes")
	cmd.Flags().StringVar(&providerCfg.namespace, "namespace", envOr("BRICKIE_SANDBOX_NAMESPACE", "default"), "kubernetes namespace for sandboxes")
	cmd.Flags().StringVar(&providerCfg.image, "image", envOr("BRICKIE_SANDBOX_IMAGE", ""), "default sandbox image")
	cmd.Flags().StringVar(&providerCfg.credentials, "credentials", envOr("BRICKIE_CREDENTIALS", ""), "comma-separated env var names to inject into sandboxes")
	cmd.Flags().StringVar(&providerCfg.snapshotDir, "snapshot-dir", envOr("BRICKIE_SNAPSHOT_DIR", engine.DefaultSnapshotDir()), "snapshot storage directory")
	cmd.Flags().DurationVar(&providerCfg.turnTimeout, "turn-timeout", defaultTimeout, "execution timeout per sandbox turn")
	cmd.Flags().StringVar(&providerCfg.kubeContext, "context", envOr("BRICKIE_KUBE_CONTEXT", ""), "kubeconfig context (kubernetes driver)")
	cmd.Flags().IntVar(&providerCfg.slots, "slots", 10, "concurrent sandbox turns")
}

var providerCfg providerConfig

func (c providerConfig) buildDriver() (engine.Driver, error) {
	switch c.driver {
	case "local":
		return local.New(), nil
	case "kubernetes":
		creds := []string{}
		for _, name := range strings.Split(c.credentials, ",") {
			if name = strings.TrimSpace(name); name != "" {
				creds = append(creds, name)
			}
		}
		return kubernetes.New(kubernetes.Options{
			Context:     c.kubeContext,
			Namespace:   c.namespace,
			Image:       c.image,
			Credentials: creds,
		})
	default:
		return nil, fmt.Errorf("unknown driver %q (local|kubernetes)", c.driver)
	}
}

func runProvider(ctx context.Context, c providerConfig) error {
	driver, err := c.buildDriver()
	if err != nil {
		return err
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	task := engine.NewProviderTask(client, engine.ProviderOpts{
		Driver:      driver,
		Store:       engine.FSSnapshotStore{Dir: c.snapshotDir},
		TurnTimeout: c.turnTimeout,
	})
	w, err := client.NewWorker("hatchet-sandbox-provider", hatchet.WithWorkflows(task), hatchet.WithDurableSlots(c.slots))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "brickie: sandbox provider started (driver=%s)\n", c.driver)
	return w.StartBlocking(ctx)
}

func workerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the sandbox provider as a Hatchet worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := cmdutils.NewInterruptContext()
			defer cancel()
			return runProvider(ctx, providerCfg)
		},
	}
	providerCfg.registerFlags(cmd)
	return cmd
}
