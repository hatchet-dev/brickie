package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	"github.com/spf13/cobra"

	"github.com/hatchet-dev/brickie/internal/driver/docker"
	"github.com/hatchet-dev/brickie/internal/driver/fsops"
	"github.com/hatchet-dev/brickie/internal/engine"
	"github.com/hatchet-dev/brickie/internal/image"
)

func envDurationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envInt64Or(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type providerConfig struct {
	image       string
	snapshotDir string
	idleTTL     time.Duration
	maxIdleTTL  time.Duration
	maxFile     int64
	objectStore string
	slots       int
}

var providerCfg providerConfig

func (c providerConfig) registerFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&providerCfg.image, "image", envOr("BRICKIE_SANDBOX_IMAGE", "alpine:3"), "default sandbox image")
	cmd.Flags().StringVar(&providerCfg.snapshotDir, "snapshot-dir", envOr("BRICKIE_SNAPSHOT_DIR", engine.DefaultSnapshotDir()), "snapshot storage directory")
	defaultSlots := 10
	if v := os.Getenv("BRICKIE_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			defaultSlots = n
		}
	}
	cmd.Flags().IntVar(&providerCfg.slots, "slots", defaultSlots, "concurrent sandbox turns")
	defaultIdle := 15 * time.Minute
	if v := os.Getenv("BRICKIE_SANDBOX_IDLE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			defaultIdle = d
		}
	}
	cmd.Flags().DurationVar(&providerCfg.idleTTL, "sandbox-idle-ttl", defaultIdle, "destroy a sandbox after this long without a turn")
	cmd.Flags().DurationVar(&providerCfg.maxIdleTTL, "max-sandbox-idle-ttl", envDurationOr("BRICKIE_MAX_SANDBOX_IDLE_TTL", 24*time.Hour), "longest idle ttl a sandbox may ask for at create")
	cmd.Flags().StringVar(&providerCfg.objectStore, "object-store", envOr("BRICKIE_OBJECT_STORE", ""), "object store URL that carries files larger than --max-file-bytes between the SDK and sandboxes: s3://bucket, gs://bucket, azblob://container, or s3://bucket?endpoint=...&use_path_style=true for S3-compatible stores")
	cmd.Flags().Int64Var(&providerCfg.maxFile, "max-file-bytes", envInt64Or("BRICKIE_MAX_FILE_BYTES", fsops.DefaultMaxFileBytes), "largest file files.read and files.write carry through a Hatchet task; the engine must allow the base64 payload (SERVER_GRPC_MAX_MSG_SIZE, default 4 MiB)")
}

func runProvider(ctx context.Context, c providerConfig) error {
	store := engine.FSSnapshotStore{Dir: c.snapshotDir}
	store.Prune(7 * 24 * time.Hour)
	if c.maxFile > 3<<20 {
		fmt.Fprintf(os.Stderr, "brickie: --max-file-bytes %d exceeds 3 MiB; set SERVER_GRPC_MAX_MSG_SIZE on the Hatchet engine to at least %d or file turns fail with a gRPC size error\n", c.maxFile, c.maxFile*4/3+1<<20)
	}
	var staging *engine.Staging
	if c.objectStore != "" {
		s, err := engine.OpenStaging(ctx, c.objectStore)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		staging = s
		fmt.Fprintf(os.Stderr, "brickie: large files are staged through the object store\n")
	}
	driver, err := docker.New(docker.Options{IdleTTL: c.idleTTL, MaxFileBytes: c.maxFile, Staging: stagingOrNil(staging)})
	if err != nil {
		return err
	}
	defer driver.Close()
	opts := engine.ProviderOpts{
		Driver:        driver,
		Store:         store,
		ImageProvider: image.Registry{Default: c.image},
		MaxIdleTTL:    c.maxIdleTTL,
		Staging:       staging,
	}
	return engine.RunOperatorProvider(ctx, opts, c.slots)
}

func stagingOrNil(s *engine.Staging) fsops.Staging {
	if s == nil {
		return nil
	}
	return s
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
