# brickie architecture

brickie is a universal sandbox API and session manager for AI agents. It deploys into the user's own cloud account and uses Hatchet for orchestration. This document describes the design and where each part lives in the repository.

## The bet

Sandboxes are a complement to orchestration. Companies that need sandboxes also need durable orchestration, retries, and observability, which is what Hatchet sells. brickie is therefore not trying to win on isolation primitives or filesystem technology. It is trying to be the easiest sandbox provider to run in your own Kubernetes cluster, on EKS, GKE, AKS, or kind, built from off-the-shelf parts: kubernetes-sigs/agent-sandbox for the pod runtime, overlayfs for snapshots, and Hatchet for everything stateful.

## State model

Agent state has three layers. We snapshot the two cheap ones and defer the expensive one.

| Layer | Owner | Mechanism |
| --- | --- | --- |
| Agent loop state (messages, tool calls, results) | Hatchet durable execution | Event log and replay |
| Filesystem state | brickie node components | overlayfs layer chain, object store blobs |
| Process memory | phase 2, optional | gVisor or CRIU, per cloud where available |

The durable log answers "what has the agent decided and observed". The filesystem snapshot answers "what does the world look like now". Every turn returns its snapshot id as part of the task output, so the durable log indexes the filesystem history.

## Turn lifecycle

A turn is one durable Hatchet task named `brickie-sandbox`. The input carries the function to run (`$command`, `$prompt`, or a user function), the input payload, and an optional snapshot id. The provider:

1. Restores the workspace from the snapshot (empty workspace when no snapshot id is given).
2. Runs the turn in a sandbox.
3. Saves the workspace as a new snapshot named after the run id.
4. Returns `{ output, snapshotId }`.

Retries are safe because the snapshot id in the input never changes and the output snapshot is keyed by run id. A retried turn starts from the same filesystem state and overwrites its own snapshot. A fork is two turns started from the same snapshot id. This makes arbitrary shell commands idempotent from the orchestrator's point of view.

Checkpoint granularity is one snapshot per turn. Large outputs should go to the object store, not into the task result.

## Components

### SDKs (`sdks/typescript`, `sdks/python`, `sdks/go`)

Thin clients. They spawn `brickie-sandbox` durable child tasks by name from the caller's durable context and return typed results. They make no placement decisions:

- `HATCHET_CLIENT_TOKEN` set: the tenant's registered provider runs the turns.
- No token: the SDK starts an embedded Hatchet engine and spawns `brickie worker --driver local` as a sidecar.

The TypeScript SDK also supports `createFunction`, which serializes a JavaScript function into the turn input (`fnSource`). The engine runs it inside the sandbox with a small node runner, so any provider can execute user functions.

### Engine (`internal/engine`)

Defines the turn contract, the durable task, and the snapshot store interface. The current store is a directory of gzipped tars keyed by run id. The overlayfs path below replaces it for Kubernetes deployments.

### Drivers (`internal/driver`)

- `local`: Docker Sandboxes through the `sbx` CLI. Used for laptop development. Docker publishes no Go client, so the CLI is the interface.
- `kubernetes`: creates one agent-sandbox `Sandbox` resource per turn using the official v1beta1 types, waits for the pod, executes over the Kubernetes exec API, and deletes the resource when the turn ends.

### Controller (`internal/controller`, `brickie controller`)

The controller process is both a Kubernetes controller and the Hatchet worker. One binary, one deployment:

- As a Hatchet worker it registers the `brickie-sandbox` durable task and executes turns with the kubernetes driver.
- As a controller it reconciles `SandboxSession` and `WorkspaceSnapshot` resources (group `brickie.hatchet.run/v1alpha1`). Sessions are the long-lived sandbox concept: TTL handling, pod ownership, and snapshot lineage records live here as the implementation fills in.

This shape means the cluster only ever dials out to Hatchet. There is no inbound API into the customer account.

### Overlay workspace engine (`internal/overlay`)

Implements the filesystem snapshot mechanics:

```
<data-dir>/
  blobs/sha256-*.tar.gz     immutable layer blobs
  layers/sha256-*/          extracted read-only layers, content-addressed
  snapshots/<id>.json       manifest: ordered list of layer digests
  sandboxes/<id>/{upper,work,merged}
```

- Mount: build the lowerdir chain from a snapshot manifest, mount overlay with a fresh upper.
- Checkpoint: `syncfs` while mounted, unmount, tar the upper, hash it, store it as a new layer, write a manifest for the new snapshot id, remount with the extended chain and an empty upper. The syncfs step keeps the agent-visible pause around tens of milliseconds instead of hundreds.
- Fork: mount the same chain with a fresh upper. No data is copied.
- Restore on another node: pull missing layer blobs from the object store, extract, mount.

Measured on ext4 with a single vCPU, a delta checkpoint of a 450 MB workspace takes about 380 ms and a fork about 4 ms. Full checkpoints and cold restores are tar-bound. See the design notes for the full benchmark table.

### Object store (`internal/overlay/remote.go`)

Layer blobs and snapshot manifests are offloaded to an object store so any node can restore any snapshot and local directories act as a cache. The store is opened by URL through the Go CDK: `s3://` for AWS, `gs://` for Google Cloud Storage, `azblob://` for Azure Blob, and `s3://` with an `endpoint` query parameter for any S3-compatible store such as MinIO or R2. Keys follow `blobs/sha256/<digest>` and `snapshots/<id>.json`. Blobs are immutable and deduplicated by content hash. Garbage collection is a mark and sweep from the manifests.

A planned change is to produce layers with `containerd/archive` so they are byte-for-byte OCI layers. That allows pushing snapshots to any OCI registry and running them with plain docker for debugging.

### CSI driver (`internal/csi`, `brickie csi-node`)

A node plugin that mounts overlay workspaces into sandbox pods as CSI ephemeral volumes. The volume context carries the sandbox id and the snapshot id. Publish mounts the chain and binds the merged directory onto the kubelet target path. `podInfoOnMount` is enabled so the driver can authorize mounts per pod. The plugin also serves a small HTTP API (`POST /v1/checkpoint`) that the provider calls between turns to checkpoint a live workspace without unmounting it from the pod.

The DaemonSet is disabled by default in the chart (`csi.enabled`). Until it is enabled, the kubernetes driver uses the tar-over-exec path from the engine store.

## Credentials

The current mechanism is an allowlist: the provider is configured with environment variable names, and only those variables are copied from the provider process into sandbox pods.

The target mechanism is a sidecar proxy, because a token an in-sandbox process can use is a token it can read. Credentials become placeholders in the sandbox environment, all egress goes through the sidecar, and the sidecar swaps placeholders for real values only on allowlisted hosts. Hatchet traffic goes through the same sidecar, which pins the sandbox to its parent run and enforces a trigger allowlist. Secrets that must be in-process (database URLs, AWS SigV4) are issued short-lived and revoked at destroy.

## Deployment

One Helm chart (`helm/brickie`) installs:

- the controller Deployment (Hatchet worker plus reconcilers), with the Hatchet token required at install time,
- the brickie CRDs,
- the agent-sandbox controller and CRDs as a vendored subchart, skippable when the cluster already has them,
- the CSI node DaemonSet, off by default.

`brickie bootstrap-kind` produces a working local cluster from nothing. `brickie bootstrap-local` configures Docker Sandboxes for laptop use without any cluster.

## Roadmap

1. Overlay workspaces wired into the kubernetes driver via the CSI path, replacing tar-over-exec.
2. Long-lived sessions reconciled by the controller: mount once, checkpoint between turns through the node HTTP API, suspend by deleting the pod and keeping the chain.
3. Credential sidecar as described above.
4. Sandbox-scoped Hatchet tokens issued through Kubernetes service account OIDC federation.
5. Image build paths: template snapshots first (fork base, run steps, checkpoint), BuildKit for full images, eventually the whole rootfs as a layer chain.
6. Memory snapshots where the platform provides them (gVisor checkpoint on GKE today, upstream pod checkpointing when it lands). These slot in as driver capabilities and do not change the SDK contract.
