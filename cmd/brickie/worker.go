package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/spf13/cobra"

	"github.com/hatchet-dev/brickie/internal/driver/kubernetes"
	"github.com/hatchet-dev/brickie/internal/driver/local"
	"github.com/hatchet-dev/brickie/internal/engine"
	"github.com/hatchet-dev/brickie/internal/injector"
	"github.com/hatchet-dev/brickie/internal/overlay"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type providerConfig struct {
	driver         string
	namespace      string
	image          string
	credentials    string
	snapshotDir    string
	objectStore    string
	kubeContext    string
	turnTimeout    time.Duration
	slots          int
	overlay        bool
	injector       bool
	injectorSecret string
	injectorImage  string
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
	cmd.Flags().StringVar(&providerCfg.objectStore, "object-store", envOr("BRICKIE_OBJECT_STORE", ""), "object store URL for turn snapshots: s3://, gs://, azblob://, or s3:// with ?endpoint= for any S3-compatible store")
	cmd.Flags().DurationVar(&providerCfg.turnTimeout, "turn-timeout", defaultTimeout, "execution timeout per sandbox turn")
	cmd.Flags().StringVar(&providerCfg.kubeContext, "context", envOr("BRICKIE_KUBE_CONTEXT", ""), "kubeconfig context (kubernetes driver)")
	defaultSlots := 10
	if v := os.Getenv("BRICKIE_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			defaultSlots = n
		}
	}
	cmd.Flags().IntVar(&providerCfg.slots, "slots", defaultSlots, "concurrent sandbox turns")
	cmd.Flags().BoolVar(&providerCfg.overlay, "overlay", os.Getenv("BRICKIE_OVERLAY") == "true", "use overlayfs workspaces via the brickie CSI driver (kubernetes driver only)")
	cmd.Flags().BoolVar(&providerCfg.injector, "injector", os.Getenv("BRICKIE_INJECTOR") == "true", "run the credential injector sidecar in sandbox pods and seal credentials instead of injecting raw values")
	cmd.Flags().StringVar(&providerCfg.injectorSecret, "injector-secret", envOr("BRICKIE_INJECTOR_SECRET", ""), "kubernetes Secret holding BRICKIE_INJECTOR_PUBLIC_KEY and BRICKIE_INJECTOR_PRIVATE_KEY")
	cmd.Flags().StringVar(&providerCfg.injectorImage, "injector-image", envOr("BRICKIE_INJECTOR_IMAGE", ""), "image for the injector sidecar (usually the brickie image)")
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
		if err := injector.ExtendCatalog(os.Getenv("BRICKIE_INJECTOR_CATALOG")); err != nil {
			return nil, err
		}
		return kubernetes.New(kubernetes.Options{
			Context:     c.kubeContext,
			Namespace:   c.namespace,
			Image:       c.image,
			Credentials: creds,
			Overlay:     c.overlay,
			Injector: kubernetes.InjectorOptions{
				Enabled:    c.injector,
				SecretName: c.injectorSecret,
				Image:      c.injectorImage,
				PublicKey:  os.Getenv("BRICKIE_INJECTOR_PUBLIC_KEY"),
			},
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
	var store engine.SnapshotStore = engine.FSSnapshotStore{Dir: c.snapshotDir}
	if c.objectStore != "" {
		remote, err := overlay.NewBucketStore(ctx, c.objectStore)
		if err != nil {
			return err
		}
		store = engine.ObjectSnapshotStore{Cache: engine.FSSnapshotStore{Dir: c.snapshotDir}, Remote: remote}
		fmt.Fprintf(os.Stderr, "brickie: turn snapshots offloaded to object store\n")
	}
	task := engine.NewProviderTask(client, engine.ProviderOpts{
		Driver:      driver,
		Store:       store,
		TurnTimeout: c.turnTimeout,
	})
	w, err := client.NewWorker("brickie-provider", hatchet.WithWorkflows(task), hatchet.WithDurableSlots(c.slots))
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
