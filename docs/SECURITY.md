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
- Every request carries a bearer token. sandboxd holds only the SHA-256 of the token, given as a flag. The token lives in the Secret `brickie-sandboxd` in the sandbox namespace, readable by the provider's service account. Rotate it by deleting the Secret and restarting the provider; existing warm pods keep the old digest until the template changes, so delete the warm pools too.
- The connection is plain HTTP and plain gRPC inside the cluster network. The token is the only protection against a pod on the same network that reaches a sandbox port. Keep the NetworkPolicy from the Helm chart, which allows ingress to sandbox pods only from the provider.
- Each turn on a sandbox is serialized. Two turns never run on one sandbox at the same time.

### Sandbox to the outside

- A sandbox pod has no service account token (`automountServiceAccountToken: false`), no host environment variables, no privilege escalation, and the runtime default seccomp profile. On a cluster with the `kata-clh` or `gvisor` runtime class, the pod runs under it. Set `--runtime` to require one; `auto` falls back to the default runtime with a warning in the log.
- The sandbox needs no network. `files.download` and `files.upload` run on the provider, not in the sandbox. The provider fetches or sends the bytes and refuses URLs that resolve to loopback, private, link-local, or unspecified addresses, and refuses redirects that leave https. Give the sandbox egress only through the NetworkPolicy in the chart.
- Linux capabilities are not dropped from the sandbox container, because package managers in common images need them and the runtime class is the isolation layer. On a cluster without Kata or gVisor, add `capabilities.drop: [ALL]` to the template in `internal/driver/kubernetes` or do not run untrusted code.

### Files and snapshots

- Every file path is confined twice: the provider rejects paths that leave the workspace, and sandboxd resolves symlinks and rejects escapes with 403.
- A snapshot is a tar made by `tar` inside the sandbox. It is extracted inside the sandbox, never on the provider. A hostile archive can only fill the sandbox's own workspace volume. Set an `emptyDir.sizeLimit` on the workspace through a LimitRange if that matters.
- The store holds snapshots, metadata, and staged files. Only the provider has its credentials. On `file://` the SDK must be on the same machine; this is for development only.
- The provider deletes store objects older than 7 days at start.

### Kubernetes permissions

The provider's Role in the sandbox namespace:

| Resource | Verbs | Why |
| --- | --- | --- |
| `sandboxclaims` | get, list, create, patch, delete | one claim per sandbox; patch extends the idle deadline |
| `sandboxtemplates`, `sandboxwarmpools` | get, create, update | one pool per image |
| `secrets` | get, create | the sandboxd token |

Plus a ClusterRole with `get` on `runtimeclasses` to detect Kata and gVisor. Nothing on pods, events, or nodes.

The agent-sandbox controller creates the pods. Its permissions are documented upstream.

### Lifecycle

- A sandbox expires after its idle TTL. The provider sets `spec.lifecycle.shutdownTime` on the claim after every turn, and the agent-sandbox controller deletes the pod when the time passes, even if the provider is down. `kill()` deletes the claim at once.
- A pod that was replaced (node drain, eviction) loses its workspace. The provider detects this through a marker file and refuses to run a turn on it. `connect` restores the latest snapshot into the new pod.

## What to check when a boundary moves

- A new way for the provider to reach a sandbox: it must carry the token and must not need pods access.
- A new place where bytes from the sandbox are processed on the provider: they must be treated as hostile.
- A new pod spec field: keep `automountServiceAccountToken: false`, `allowPrivilegeEscalation: false`, and the seccomp profile.
- A new URL the provider fetches on behalf of a caller: it must go through the guarded client in `internal/driver/fsops`.
