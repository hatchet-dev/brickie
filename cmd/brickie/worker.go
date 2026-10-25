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
	"github.com/hatchet-dev/brickie/internal/driver/kubernetes"
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

func envIntOr(key string, fallback int64) int64 {
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
	driver       string
	image        string
	store        string
	idleTTL      time.Duration
	maxIdleTTL   time.Duration
	maxFile      int64
	slots        int
	namespace    string
	runtime      string
	warmReplicas int
}

var providerCfg providerConfig

func (c providerConfig) registerFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&providerCfg.driver, "driver", envOr("BRICKIE_DRIVER", "docker"), "where sandboxes run: docker (the local engine) or kubernetes (agent-sandbox claims)")
	f.StringVar(&providerCfg.image, "image", envOr("BRICKIE_SANDBOX_IMAGE", "alpine:3"), "default sandbox image")
	f.StringVar(&providerCfg.store, "store", envOr("BRICKIE_STORE", engine.DefaultStoreURL()), "bucket for snapshots, sandbox metadata, and files larger than --max-file-bytes: s3://bucket, gs://bucket, azblob://container, s3://bucket?endpoint=...&use_path_style=true for S3-compatible stores, or file:///dir on one machine")
	f.Int64Var(&providerCfg.maxFile, "max-file-bytes", envIntOr("BRICKIE_MAX_FILE_BYTES", fsops.DefaultMaxFileBytes), "largest file files.read and files.write carry through a Hatchet task; the engine must allow the base64 payload (SERVER_GRPC_MAX_MSG_SIZE, default 4 MiB)")
	f.DurationVar(&providerCfg.idleTTL, "sandbox-idle-ttl", envDurationOr("BRICKIE_SANDBOX_IDLE_TTL", 15*time.Minute), "destroy a sandbox after this long without a turn")
	f.DurationVar(&providerCfg.maxIdleTTL, "max-sandbox-idle-ttl", envDurationOr("BRICKIE_MAX_SANDBOX_IDLE_TTL", 24*time.Hour), "longest idle ttl a sandbox may ask for at create")
	f.IntVar(&providerCfg.slots, "slots", int(envIntOr("BRICKIE_SLOTS", 10)), "concurrent sandbox turns")
	f.StringVar(&providerCfg.namespace, "namespace", envOr("BRICKIE_SANDBOX_NAMESPACE", "default"), "kubernetes: namespace for sandbox pods and claims")
	f.StringVar(&providerCfg.runtime, "runtime", envOr("BRICKIE_SANDBOX_RUNTIME", "auto"), "kubernetes: runtime class for sandbox pods: auto (kata, then gvisor), kata, gvisor, or none")
	f.IntVar(&providerCfg.warmReplicas, "warm-replicas", int(envIntOr("BRICKIE_WARM_REPLICAS", 2)), "kubernetes: pre-started pods kept per image")
}

func runProvider(ctx context.Context, c providerConfig) error {
	store, err := engine.OpenStore(ctx, c.store)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	store.Prune(ctx, 7*24*time.Hour)
	if c.maxFile > 3<<20 {
		fmt.Fprintf(os.Stderr, "brickie: --max-file-bytes %d exceeds 3 MiB; set SERVER_GRPC_MAX_MSG_SIZE on the Hatchet engine to at least %d or file turns fail with a gRPC size error\n", c.maxFile, c.maxFile*4/3+1<<20)
	}
	var drv engine.Driver
	switch c.driver {
	case "docker":
		d, err := docker.New(docker.Options{IdleTTL: c.idleTTL, MaxFileBytes: c.maxFile, Staging: store})
		if err != nil {
			return err
		}
		defer d.Close()
		drv = d
	case "kubernetes":
		if store.Local() {
			fmt.Fprintf(os.Stderr, "brickie: --store is a directory inside the provider pod; files larger than --max-file-bytes need an object store\n")
		}
		d, err := kubernetes.New(kubernetes.Options{Namespace: c.namespace, Image: c.image, Runtime: c.runtime, WarmReplicas: c.warmReplicas, IdleTTL: c.idleTTL, MaxFileBytes: c.maxFile, Staging: store})
		if err != nil {
			return err
		}
		drv = d
	default:
		return fmt.Errorf("unknown driver %q (docker|kubernetes)", c.driver)
	}
	return engine.RunOperatorProvider(ctx, engine.ProviderOpts{
		Driver:        drv,
		Store:         store,
		ImageProvider: image.Registry{Default: c.image},
		MaxIdleTTL:    c.maxIdleTTL,
	}, c.slots)
}

func workerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the sandbox provider as a Hatchet gRPC operator",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := cmdutils.NewInterruptContext()
			defer cancel()
			return runProvider(ctx, providerCfg)
		},
	}
	providerCfg.registerFlags(cmd)
	return cmd
}
