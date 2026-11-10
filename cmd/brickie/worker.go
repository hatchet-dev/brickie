package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/hatchet-dev/brickie/internal/driver/docker"
	"github.com/hatchet-dev/brickie/internal/driver/fsops"
	"github.com/hatchet-dev/brickie/internal/driver/kubernetes"
	"github.com/hatchet-dev/brickie/internal/engine"
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
	driver            string
	image             string
	store             string
	idleTTL           time.Duration
	maxIdleTTL        time.Duration
	maxFile           int64
	maxSnapshot       int64
	slots             int
	namespace         string
	runtime           string
	warmReplicas      int
	providerNamespace string
	providerSelector  string
	providerImage     string
	egress            string
	cpu, memory, disk string
	nodeSelector      string
	tolerations       string
}

var providerCfg providerConfig

func registerFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&providerCfg.driver, "driver", envOr("BRICKIE_DRIVER", "docker"), "where sandboxes run: docker (the local engine) or kubernetes (agent-sandbox claims)")
	f.StringVar(&providerCfg.image, "image", envOr("BRICKIE_SANDBOX_IMAGE", "alpine:3"), "default sandbox image")
	f.StringVar(&providerCfg.store, "store", envOr("BRICKIE_STORE", engine.DefaultStoreURL()), "bucket for snapshots, sandbox metadata, and files larger than --max-file-bytes: s3://bucket, gs://bucket, azblob://container, s3://bucket?endpoint=...&use_path_style=true for S3-compatible stores, or file:///dir on one machine")
	f.Int64Var(&providerCfg.maxFile, "max-file-bytes", envIntOr("BRICKIE_MAX_FILE_BYTES", fsops.DefaultMaxFileBytes), "largest file files.read and files.write carry through a Hatchet task; the engine must allow the base64 payload (SERVER_GRPC_MAX_MSG_SIZE, default 4 MiB)")
	f.DurationVar(&providerCfg.idleTTL, "sandbox-idle-ttl", envDurationOr("BRICKIE_SANDBOX_IDLE_TTL", 15*time.Minute), "destroy a sandbox after this long without a turn")
	f.DurationVar(&providerCfg.maxIdleTTL, "max-sandbox-idle-ttl", envDurationOr("BRICKIE_MAX_SANDBOX_IDLE_TTL", 24*time.Hour), "longest idle ttl a sandbox may ask for at create")
	f.IntVar(&providerCfg.slots, "slots", int(envIntOr("BRICKIE_SLOTS", 10)), "concurrent sandbox turns")
	f.StringVar(&providerCfg.namespace, "namespace", envOr("BRICKIE_SANDBOX_NAMESPACE", "default"), "kubernetes: namespace for sandbox pods and claims")
	f.StringVar(&providerCfg.runtime, "runtime", envOr("BRICKIE_SANDBOX_RUNTIME", "auto"), "kubernetes: runtime class for sandbox pods: auto (kata, gvisor, then nitro), kata, gvisor, nitro (EKS isolated-sandbox), none, or any RuntimeClass name on the cluster")
	f.IntVar(&providerCfg.warmReplicas, "warm-replicas", int(envIntOr("BRICKIE_WARM_REPLICAS", 2)), "kubernetes: pre-started pods kept per image")
	f.Int64Var(&providerCfg.maxSnapshot, "max-snapshot-bytes", envIntOr("BRICKIE_MAX_SNAPSHOT_BYTES", 1<<30), "largest workspace snapshot a turn may produce")
	f.StringVar(&providerCfg.providerNamespace, "provider-namespace", envOr("BRICKIE_PROVIDER_NAMESPACE", ""), "kubernetes: namespace of this provider pod and its sandboxd token secret (default: the pod's own)")
	f.StringVar(&providerCfg.providerSelector, "provider-selector", envOr("BRICKIE_PROVIDER_SELECTOR", "app.kubernetes.io/name=brickie"), "kubernetes: labels of this provider pod; only pods matching them may reach sandboxes")
	f.StringVar(&providerCfg.providerImage, "provider-image", envOr("BRICKIE_PROVIDER_IMAGE", ""), "kubernetes: this provider's own image, run in every sandbox pod as the TLS proxy in front of sandboxd (brickie sandbox-tls)")
	f.StringVar(&providerCfg.egress, "sandbox-egress", envOr("BRICKIE_SANDBOX_EGRESS", "internet"), "kubernetes: what sandboxes may reach: internet (public addresses and cluster DNS) or none")
	f.StringVar(&providerCfg.nodeSelector, "sandbox-node-selector", envOr("BRICKIE_SANDBOX_NODE_SELECTOR", ""), "kubernetes: node labels sandbox pods are placed on, as key=value pairs separated by commas")
	f.StringVar(&providerCfg.tolerations, "sandbox-tolerations", envOr("BRICKIE_SANDBOX_TOLERATIONS", ""), "kubernetes: tolerations for sandbox pods, as a JSON list of Kubernetes tolerations")
	f.StringVar(&providerCfg.cpu, "sandbox-cpu", envOr("BRICKIE_SANDBOX_CPU", "1"), "kubernetes: cpu limit per sandbox")
	f.StringVar(&providerCfg.memory, "sandbox-memory", envOr("BRICKIE_SANDBOX_MEMORY", "2Gi"), "kubernetes: memory limit per sandbox")
	f.StringVar(&providerCfg.disk, "sandbox-disk", envOr("BRICKIE_SANDBOX_DISK", "10Gi"), "kubernetes: ephemeral storage limit per sandbox, including the workspace")
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
		selector, err := labels.ConvertSelectorToLabelsMap(c.providerSelector)
		if err != nil {
			return fmt.Errorf("--provider-selector: %w", err)
		}
		opts := kubernetes.Options{Namespace: c.namespace, Image: c.image, Runtime: c.runtime, WarmReplicas: c.warmReplicas, IdleTTL: c.idleTTL, MaxFileBytes: c.maxFile, Staging: store, ProviderNamespace: c.providerNamespace, ProviderSelector: selector, ProviderImage: c.providerImage, Egress: c.egress}
		for q, s := range map[*resource.Quantity]string{&opts.CPU: c.cpu, &opts.Memory: c.memory, &opts.Disk: c.disk} {
			if *q, err = resource.ParseQuantity(s); err != nil {
				return fmt.Errorf("sandbox resource %q: %w", s, err)
			}
		}
		if c.nodeSelector != "" {
			if opts.NodeSelector, err = labels.ConvertSelectorToLabelsMap(c.nodeSelector); err != nil {
				return fmt.Errorf("--sandbox-node-selector: %w", err)
			}
		}
		if c.tolerations != "" {
			if err := json.Unmarshal([]byte(c.tolerations), &opts.Tolerations); err != nil {
				return fmt.Errorf("--sandbox-tolerations: %w", err)
			}
		}
		d, err := kubernetes.New(opts)
		if err != nil {
			return err
		}
		drv = d
	default:
		return fmt.Errorf("unknown driver %q (docker|kubernetes)", c.driver)
	}
	return engine.RunOperatorProvider(ctx, engine.ProviderOpts{
		Driver:           drv,
		Store:            store,
		DefaultImage:     c.image,
		MaxIdleTTL:       c.maxIdleTTL,
		MaxSnapshotBytes: c.maxSnapshot,
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
	registerFlags(cmd)
	return cmd
}
