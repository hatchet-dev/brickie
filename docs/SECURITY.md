# Security model

This document describes what brickie trusts, where the boundaries are, and which control holds each boundary. Read it before you change a driver, the store, or the sandbox pod shape.

## Components

| Component | Runs where | Trusts |
| --- | --- | --- |
| SDK | the customer's application | the Hatchet tenant token it was given |
| Hatchet | Hatchet Cloud or a self-hosted engine | tenant tokens |
| Provider (`brickie worker`) | a pod in the customer's cluster, or a developer machine | its Hatchet token, its Kubernetes service account, its store credentials |
| sandboxd | PID 1 in every sandbox | a bearer token known only to the provider |
| Sandbox | a pod (Kubernetes) or a container (Docker) | nothing |

The agent's code runs in the sandbox. Everything the sandbox can reach is the attack surface of the agent.

## Boundaries and controls

### SDK to Hatchet

- Every SDK call is one `brickie-sandbox` task run in the caller's tenant. The SDK has no credentials other than the tenant token. It never talks to the provider, the store, or a sandbox directly.
- A caller-supplied sandbox ID must match `^[a-z0-9][a-z0-9-]{0,62}$`. The provider rejects everything else before it reaches a driver or the API server.
- Large files travel on presigned URLs that the provider mints for one object key. A key belongs to one sandbox. A write that names a key of another sandbox is refused. URLs expire after 15 minutes.

### Hatchet to provider

- The provider is a gRPC operator. It serves only the `brickie-sandbox` action of the tenant its token belongs to. Run one provider per tenant. Two tenants must not share a provider, because the provider's store and sandbox namespace are not partitioned by tenant.
- The provider accepts the sandbox ID, image, function, and input from the task. It validates the ID and the paths. It does not validate the image. Restrict images at the cluster level with an admission policy if the tenant must not pull arbitrary images.

### Provider to sandbox

- The provider talks only to sandboxd: commands over gRPC, files over REST, on the pod IP (Kubernetes) or a loopback port (Docker). It does not use `pods/exec` and needs no access to pods.
- Every request carries a bearer token. sandboxd holds only the SHA-256 of the token, given as a flag. The token lives in the Secret `brickie-sandboxd` in the provider's own namespace (`--provider-namespace`), never in the sandbox namespace, readable by the provider's service account. Rotate it by deleting the Secret and restarting the provider; existing warm pods keep the old digest until the template changes, so delete the warm pools too.
- The connection is plain HTTP and plain gRPC inside the cluster network; sandboxd v1.0.8 cannot terminate TLS. Two controls stand in: the token, and the NetworkPolicy the agent-sandbox controller keeps for every template, which the provider writes into the template so that only pods matching `--provider-selector` in the provider's namespace may reach ports 8080 and 9090. It holds only where the CNI enforces NetworkPolicy. Encrypting this hop needs either a TLS sidecar in the sandbox pod or TLS in sandboxd; it is the open item in this model.
- The sandboxd client never follows redirects. A sandbox that answers with one gets an error, not a request to the address it chose.
- Each turn on a sandbox is serialized in the provider, from execution through snapshot publication. Two turns never run on one sandbox at the same time, and the latest-snapshot pointer never moves backwards.
- Everything a sandbox sends back is bounded on the provider: command stdout and stderr are kept up to 1 MiB each and marked truncated beyond that, inline file reads stop at `--max-file-bytes` no matter what size the listing claimed, and a snapshot streams to the store and fails past `--max-snapshot-bytes` (1 GiB by default) without ever being held in memory.

### Sandbox to the outside

- A sandbox pod has no service account token (`automountServiceAccountToken: false`), no host environment variables, no privilege escalation, and the runtime default seccomp profile. On a cluster with the `kata-clh` or `gvisor` runtime class, the pod runs under it. Set `--runtime` to require one; `auto` falls back to the default runtime with a warning in the log.
- The sandbox needs no network for brickie itself. `files.download` and `files.upload` run on the provider, not in the sandbox. The provider fetches or sends the bytes and refuses URLs that resolve to loopback, private, link-local, or unspecified addresses, and refuses redirects that leave https. What the agent's own code may reach is `--sandbox-egress`: `internet` allows public addresses and cluster DNS and blocks private ranges and the metadata endpoint; `none` allows nothing.
- Every sandbox has a CPU, memory, and ephemeral-storage limit (`--sandbox-cpu`, `--sandbox-memory`, `--sandbox-disk`; 1, 2Gi, 10Gi by default), and the workspace volume carries the same size limit. An agent can exhaust its own sandbox, not the node.
- Linux capabilities are not dropped from the sandbox container, because package managers in common images need them and the runtime class is the isolation layer. On a cluster without Kata or gVisor, add `capabilities.drop: [ALL]` to the template in `internal/driver/kubernetes` or do not run untrusted code.

### Files and snapshots

- Every file path is confined twice. The provider rejects paths that leave the workspace lexically and escapes each path segment once, so a name is always the literal name. Reads, writes, and listings then go through sandboxd, which resolves symlinks and rejects escapes with 403. `mkdir`, `rename`, `copy`, and `remove` run as commands inside the sandbox; each first resolves the nearest existing ancestor of its paths and refuses to act unless it lies under the workspace, and `remove` unlinks a symlink instead of following it. Only the agent itself, inside its own sandbox, could race that check, and it already owns everything outside the workspace there.
- A snapshot is a tar made by `tar` inside the sandbox. It is extracted inside the sandbox, never on the provider. A hostile archive can only fill the sandbox's own workspace volume, which has a size limit.
- The store holds snapshots, metadata, and staged files. Only the provider has its credentials. On `file://` the SDK must be on the same machine; this is for development only.
- The provider deletes its own store objects older than 7 days at start, except the metadata of a sandbox that still has a latest snapshot. Staged files are kept until then so a retried turn can still read its input; on S3 a one-day lifecycle rule on the staging prefix is the recommended shorter bound.

### Kubernetes permissions

The provider's Role in the sandbox namespace:

| Resource | Verbs | Why |
| --- | --- | --- |
| `sandboxclaims` | get, list, create, patch, delete | one claim per sandbox; patch extends the idle deadline |
| `sandboxtemplates`, `sandboxwarmpools` | get, create, update | one pool per image; the template carries the NetworkPolicy and resource limits |

Plus `get` and `create` on `secrets` in the provider's own namespace for the sandboxd token, and a ClusterRole with `get` on `runtimeclasses` to detect Kata and gVisor. Nothing on pods, events, nodes, or networkpolicies.

The agent-sandbox controller creates the pods and the NetworkPolicy. Its permissions are documented upstream.

### Lifecycle

- A sandbox expires after its idle TTL. The provider sets `spec.lifecycle.shutdownTime` on the claim before every turn, to the TTL plus the command's own timeout, and again after it, so a running command never loses its pod to expiry; the agent-sandbox controller deletes the pod when the time passes, even if the provider is down. `kill()` deletes the claim at once.
- A pod that was replaced (node drain, eviction) loses its workspace. The provider detects this through a marker file, written only after a restore succeeded, and refuses to run a turn on it. `connect` restores the latest snapshot into the new pod. A fork records its inherited snapshot as its latest at creation, so it can be connected again even before its first change.
- A failure to record the latest snapshot fails the turn after three attempts, and a store that cannot be read fails a connect, so a sandbox never silently comes back empty.

## What to check when a boundary moves

- A new way for the provider to reach a sandbox: it must carry the token and must not need pods access.
- A new place where bytes from the sandbox are processed on the provider: they must be treated as hostile.
- A new pod spec field: keep `automountServiceAccountToken: false`, `allowPrivilegeEscalation: false`, and the seccomp profile.
- A new URL the provider fetches on behalf of a caller: it must go through the guarded client in `internal/driver/fsops`.
