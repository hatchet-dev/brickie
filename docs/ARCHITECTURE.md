# brickie architecture

brickie is a universal sandbox API and session manager for AI agents. It deploys into the user's own cloud account and uses Hatchet for orchestration. This document describes the design and where each part lives in the repository.

## The bet

Sandboxes are a complement to orchestration. Companies that need sandboxes also need durable orchestration, retries, and observability, which is what Hatchet sells. brickie is therefore not trying to win on isolation primitives or filesystem technology. It is trying to be the easiest sandbox provider to run in your own Kubernetes cluster, on EKS, GKE, AKS, or kind, built from off-the-shelf parts: kubernetes-sigs/agent-sandbox for the pod runtime, gVisor or Kata Containers for isolation, overlayfs for snapshots, and Hatchet for everything stateful.

## State model

Agent state has three layers. We snapshot the two cheap ones and defer the expensive one.

| Layer | Owner | Mechanism |
| --- | --- | --- |
| Agent loop state (messages, tool calls, results) | Hatchet durable execution | Event log and replay |
| Filesystem state and conversation transcript | brickie node components | overlayfs layer chain, object store blobs |
| Process memory | phase 2, optional | gVisor or Kata checkpoint, per cloud where available |

The durable log answers "what has the agent decided and observed". The filesystem snapshot answers "what does the world look like now". Every turn returns its snapshot id as part of the task output, so the durable log indexes the filesystem history. The conversation transcript (`.conversation.json` in the workspace) rides the snapshot, so resuming a snapshot resumes the conversation.

Sandbox pods run under a runtime class the kubernetes driver selects at startup (`sandbox.runtime`: auto, gvisor, kata, none). `brickie bootstrap-aws --runtime gvisor` installs runsc on every node and creates the `gvisor` class; `--runtime kata` installs kata-deploy with the Cloud Hypervisor shim and uses the `kata-clh` class, which needs bare-metal amd64 nodes for KVM. Both compose with the snapshot design because the overlay chain lives entirely on the host. gVisor proxies file I/O into the merged mount through its gofer; Kata shares it into the guest VM with virtiofs (`cache=auto`). The driver runs `sync` inside the sandbox before each checkpoint so buffered writes land in the upper layer before the host syncfs and tar. Clusters without either class, such as kind, use the default runtime with a warning.

## Session and turn lifecycle

A session is one durable Hatchet task written by the user. Inside it, `hatchet.sandbox.create(ctx)` binds one sandbox for the life of the session, and each `exec` or `prompt` is one plain Hatchet task named `brickie-sandbox` that runs in that sandbox. The sandbox id is derived from the durable run id and creation order, so a replayed session lands in the same sandboxes.

The turn input carries the sandbox id, the function (`$create`, `$command`, `$prompt`, `$destroy`), the input payload, and for `$create` an optional snapshot id. The provider:

1. `$create`: claims a warm pod (or reuses the claim a previous attempt bound), restores the snapshot into the empty workspace, records the pod's UID on the claim.
2. `$command` / `$prompt`: resolves the session's pod, extends its idle deadline, runs the function, checkpoints the live workspace as a new snapshot named after the run id, returns `{ output, snapshotId }`.
3. `$destroy`: deletes the claim; agent-sandbox tears down the pod and the CSI node unmounts the workspace.

Idle sessions expire through the claim's `shutdownTime`, which every turn pushes out by the provider's `--sandbox-idle-ttl`; agent-sandbox deletes the claim when it passes. A turn on an expired sandbox, or on a pod that Kubernetes replaced (the claim's recorded UID no longer matches), fails with a clear error. The provider never recreates a sandbox silently: the live workspace is gone and only a new `$create` from the last snapshot brings it back.

Retries: `$create` is idempotent through claim reuse. `$command` and `$prompt` are at-least-once on a live sandbox. A retried turn re-runs in the current workspace and overwrites its own snapshot, so a non-idempotent command may run twice; the orchestrator owns that decision. A fork is a second `$create` from the same snapshot id.

Checkpoint granularity is one snapshot per turn. Large outputs should go to the object store, not into the task result.

## Components

### SDK (`sdks/typescript`)

A thin client. `hatchet.sandbox.create` returns a `Sandbox` whose `exec`, `prompt`, and `kill` spawn `brickie-sandbox` child tasks by name from the caller's durable context and return typed results. It makes no placement decisions:

- `HATCHET_CLIENT_TOKEN` set: the tenant's registered provider runs the turns.
- No token: the SDK starts an embedded Hatchet engine and spawns `brickie worker --driver local` as a sidecar.

### Engine (`internal/engine`)

Defines the turn contract, the provider task, the transcript helpers, and the snapshot store interface. The tar store is a directory of gzipped tars keyed by run id, optionally offloaded to an object store. The overlayfs path below replaces it for Kubernetes deployments with the CSI driver.

### Drivers (`internal/driver`)

- `local`: Docker Sandboxes through the `sbx` CLI. One live micro-VM per sandbox id, kept in a process-local map with an idle reaper. Docker publishes no Go client, so the CLI is the interface.
- `kubernetes`: one agent-sandbox `SandboxClaim` per sandbox id, bound from a per-image `SandboxWarmPool`. The claim is the durable session registry, so a provider restart loses nothing. Turns exec over the Kubernetes exec API. Turns on the same sandbox are serialized with a per-process lock; add a Hatchet concurrency key on `input.sandboxId` before running more than one provider replica.

### Provider (`brickie worker`)

The provider is the Hatchet worker: it registers the `brickie-sandbox` task and executes turns with the configured driver. There is deliberately no brickie Kubernetes controller or CRD. Desired state lives in Hatchet's run history and in agent-sandbox's claims, so retries, timeouts, and replay come from the orchestrator and idle expiry comes from agent-sandbox's reconciler.

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

- Mount: build the lowerdir chain from a snapshot manifest, mount overlay with a fresh upper. Sessions start from an empty chain.
- Restore: at `$create`, copy a snapshot's merged view into the live mount through a read-only overlay of its chain. The kernel resolves whiteouts during the copy.
- Checkpoint: `syncfs` while mounted, tar the upper in place, hash it, store it as a layer, write a manifest for the new snapshot id. The mount stays up. On a live session every checkpoint tars the cumulative upper, so the cost grows with the changes made in the session and earlier blobs of the same session become unreferenced. Incremental layers between turns are a measured follow-up.
- Fork: mount the same chain with a fresh upper. No data is copied.
- Restore on another node: pull missing layer blobs from the object store, extract, mount.

### Object store (`internal/overlay/remote.go`)

Layer blobs and snapshot manifests are offloaded to an object store so any node can restore any snapshot and local directories act as a cache. The store is opened by URL through the Go CDK: `s3://` for AWS, `gs://` for Google Cloud Storage, `azblob://` for Azure Blob, and `s3://` with an `endpoint` query parameter for any S3-compatible store such as MinIO or R2. Keys follow `blobs/sha256/<digest>` and `snapshots/<id>.json`. Blobs are immutable and deduplicated by content hash. Garbage collection is a mark and sweep from the manifests.

A planned change is to produce layers with `containerd/archive` so they are byte-for-byte OCI layers. That allows pushing snapshots to any OCI registry and running them with plain docker for debugging.

### CSI driver (`internal/csi`, `brickie csi-node`)

A node plugin that mounts overlay workspaces into sandbox pods as CSI ephemeral volumes keyed by pod name. Publish mounts an empty chain and binds the merged directory onto the kubelet target path; unpublish, which follows pod deletion, unmounts it. The plugin also serves a small HTTP API (`POST /v1/restore`, `POST /v1/checkpoint`) that the provider calls at `$create` and after every turn.

The DaemonSet is disabled by default in the chart (`csi.enabled`). Until it is enabled, the kubernetes driver uses the tar-over-exec path from the engine store.

## Credentials

The current mechanism is an allowlist: the provider is configured with environment variable names, and only those variables are copied from the provider process into sandbox pods.

The target mechanism is a sidecar proxy, because a token an in-sandbox process can use is a token it can read. Credentials become placeholders in the sandbox environment, all egress goes through the sidecar, and the sidecar swaps placeholders for real values only on allowlisted hosts. Hatchet traffic goes through the same sidecar, which pins the sandbox to its parent run and enforces a trigger allowlist. Secrets that must be in-process (database URLs, AWS SigV4) are issued short-lived and revoked at destroy.

## Deployment

One Helm chart (`helm/brickie`) installs:

- the provider Deployment (the Hatchet worker), with the Hatchet token required at install time,
- the agent-sandbox controller and CRDs as a vendored subchart, skippable when the cluster already has them,
- the CSI node DaemonSet, off by default.

`brickie bootstrap-kind` produces a working local cluster from nothing. `brickie bootstrap-aws` produces an EKS install with gVisor or Kata. `hack/run-demo.sh` configures Docker Sandboxes and a Compose-hosted Hatchet for laptop use without any cluster.

## Roadmap

1. Incremental overlay layers between turns of one session, so a checkpoint costs the turn's diff rather than the session's.
2. Hatchet concurrency key on the sandbox id, so the provider can run more than one replica.
3. Credential sidecar as described above.
4. Sandbox-scoped Hatchet tokens issued through Kubernetes service account OIDC federation.
5. Image build paths: template snapshots first (fork base, run steps, checkpoint), BuildKit for full images, eventually the whole rootfs as a layer chain.
6. Memory snapshots where the platform provides them (Kata and gVisor checkpoint, upstream pod checkpointing when it lands). These slot in as driver capabilities and do not change the SDK contract.
