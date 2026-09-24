package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	hatchetotel "github.com/hatchet-dev/hatchet/sdks/go/opentelemetry"
	"github.com/spf13/cobra"

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
	snapshotDir string
	turnTimeout time.Duration
	slots       int
}

var providerCfg providerConfig

func (c providerConfig) registerFlags(cmd *cobra.Command) {
	defaultTimeout := 15 * time.Minute
	if v := os.Getenv("BRICKIE_TURN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			defaultTimeout = d
		}
	}
	cmd.Flags().StringVar(&providerCfg.snapshotDir, "snapshot-dir", envOr("BRICKIE_SNAPSHOT_DIR", engine.DefaultSnapshotDir()), "snapshot storage directory")
	cmd.Flags().DurationVar(&providerCfg.turnTimeout, "turn-timeout", defaultTimeout, "execution timeout per sandbox turn")
	defaultSlots := 10
	if v := os.Getenv("BRICKIE_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			defaultSlots = n
		}
	}
	cmd.Flags().IntVar(&providerCfg.slots, "slots", defaultSlots, "concurrent sandbox turns")
}

func runProvider(ctx context.Context, c providerConfig) error {
	store := engine.FSSnapshotStore{Dir: c.snapshotDir}
	store.Prune(7 * 24 * time.Hour)
	opts := engine.ProviderOpts{
		Driver:      local.New(),
		Store:       store,
		TurnTimeout: c.turnTimeout,
	}
	err := engine.RunOperatorProvider(ctx, opts, c.slots)
	if !errors.Is(err, engine.ErrOperatorUnavailable) {
		return err
	}
	fmt.Fprintf(os.Stderr, "brickie: %v; falling back to worker mode\n", err)
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	task := engine.NewProviderTask(client, opts)
	w, err := client.NewWorker("brickie-provider", hatchet.WithWorkflows(task), hatchet.WithDurableSlots(c.slots))
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
	fmt.Fprintf(os.Stderr, "brickie: sandbox provider started\n")
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
