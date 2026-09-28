package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	hatchetotel "github.com/hatchet-dev/hatchet/sdks/go/opentelemetry"
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

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func splitList(s string) []string {
	out := []string{}
	for name := range strings.SplitSeq(s, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

type providerConfig struct {
	driver       string
	namespace    string
	image        string
	credentials  string
	snapshotDir  string
	objectStore  string
	kubeContext  string
	turnTimeout  time.Duration
	idleTTL      time.Duration
	slots        int
	runtime      string
	warmImages   string
	warmReplicas int
}

var providerCfg providerConfig

func (c providerConfig) registerFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&providerCfg.driver, "driver", envOr("BRICKIE_DRIVER", "local"), "sandbox driver: local (Docker Sandboxes) or kubernetes")
	cmd.Flags().StringVar(&providerCfg.snapshotDir, "snapshot-dir", envOr("BRICKIE_SNAPSHOT_DIR", engine.DefaultSnapshotDir()), "snapshot storage directory")
	cmd.Flags().StringVar(&providerCfg.objectStore, "object-store", envOr("BRICKIE_OBJECT_STORE", ""), "object store URL for snapshots: s3://, gs://, azblob://, or s3:// with ?endpoint= for any S3-compatible store")
	cmd.Flags().DurationVar(&providerCfg.turnTimeout, "turn-timeout", envDuration("BRICKIE_TURN_TIMEOUT", 15*time.Minute), "execution timeout per sandbox turn")
	cmd.Flags().DurationVar(&providerCfg.idleTTL, "sandbox-idle-ttl", envDuration("BRICKIE_SANDBOX_IDLE_TTL", 30*time.Minute), "destroy a sandbox after this long without a turn (must exceed --turn-timeout)")
	cmd.Flags().IntVar(&providerCfg.slots, "slots", envInt("BRICKIE_SLOTS", 10), "concurrent sandbox turns")
	cmd.Flags().StringVar(&providerCfg.namespace, "namespace", envOr("BRICKIE_SANDBOX_NAMESPACE", "default"), "kubernetes namespace for sandboxes")
	cmd.Flags().StringVar(&providerCfg.image, "image", envOr("BRICKIE_SANDBOX_IMAGE", ""), "default sandbox image (kubernetes driver)")
	cmd.Flags().StringVar(&providerCfg.credentials, "credentials", envOr("BRICKIE_CREDENTIALS", ""), "comma-separated env var names to copy into sandbox pods (kubernetes driver)")
	cmd.Flags().StringVar(&providerCfg.kubeContext, "context", envOr("BRICKIE_KUBE_CONTEXT", ""), "kubeconfig context (kubernetes driver)")
	cmd.Flags().StringVar(&providerCfg.runtime, "runtime", envOr("BRICKIE_RUNTIME", "auto"), "sandbox pod runtime class: auto (kata-clh, then gvisor, when present), gvisor, kata, or none (kubernetes driver)")
	cmd.Flags().StringVar(&providerCfg.warmImages, "warm-images", envOr("BRICKIE_WARM_IMAGES", ""), "comma-separated images to pre-create warm pools for at startup (kubernetes driver)")
	cmd.Flags().IntVar(&providerCfg.warmReplicas, "warm-replicas", envInt("BRICKIE_WARM_REPLICAS", 2), "warm sandbox pods kept ready per image (kubernetes driver)")
}

func (c providerConfig) buildDriver() (engine.Driver, error) {
	switch c.driver {
	case "local":
		return local.New(c.idleTTL), nil
	case "kubernetes":
		return kubernetes.New(kubernetes.Options{
			Context:      c.kubeContext,
			Namespace:    c.namespace,
			Image:        c.image,
			Credentials:  splitList(c.credentials),
			IdleTTL:      c.idleTTL,
			Runtime:      c.runtime,
			WarmImages:   splitList(c.warmImages),
			WarmReplicas: c.warmReplicas,
		})
	default:
		return nil, fmt.Errorf("unknown driver %q (local|kubernetes)", c.driver)
	}
}

func runProvider(ctx context.Context, c providerConfig) error {
	if c.idleTTL <= c.turnTimeout {
		return fmt.Errorf("--sandbox-idle-ttl (%s) must exceed --turn-timeout (%s) so a sandbox cannot expire mid-turn", c.idleTTL, c.turnTimeout)
	}
	driver, err := c.buildDriver()
	if err != nil {
		return err
	}
	if closer, ok := driver.(interface{ Close() }); ok {
		defer closer.Close()
	}
	fsStore := engine.FSSnapshotStore{Dir: c.snapshotDir}
	fsStore.Prune(7 * 24 * time.Hour)
	var store engine.SnapshotStore = fsStore
	if c.objectStore != "" {
		objectStore, err := engine.NewObjectSnapshotStore(ctx, c.objectStore, fsStore)
		if err != nil {
			return err
		}
		defer func() { _ = objectStore.Close() }()
		store = objectStore
		fmt.Fprintf(os.Stderr, "brickie: snapshots offloaded to object store\n")
	}
	opts := engine.ProviderOpts{
		Driver:      driver,
		Store:       store,
		TurnTimeout: c.turnTimeout,
	}
	err = engine.RunOperatorProvider(ctx, opts, c.slots)
	if !errors.Is(err, engine.ErrOperatorUnavailable) || c.driver == "kubernetes" {
		return err
	}
	fmt.Fprintf(os.Stderr, "brickie: %v; falling back to worker mode\n", err)
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	task := engine.NewProviderTask(client, opts)
	w, err := client.NewWorker("brickie-provider", hatchet.WithWorkflows(task), hatchet.WithSlots(c.slots))
	if err != nil {
		return err
	}
	if os.Getenv("BRICKIE_OTEL") != "false" {
		instrumentor, err := hatchetotel.NewInstrumentor()
		if err != nil {
			fmt.Fprintf(os.Stderr, "brickie: opentelemetry disabled: %v\n", err)
		} else {
			w.Use(instrumentor.Middleware())
			defer func() {
				_ = instrumentor.Shutdown(context.Background())
			}()
		}
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
