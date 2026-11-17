# Security model

This document describes what brickie trusts, where the boundaries are, and which control holds each boundary. Read it before you change a driver, the store, or the sandbox pod shape.

## Components

| Component | Runs where | Trusts |
| --- | --- | --- |
| SDK | the customer's application | the Hatchet tenant token it was given |
| Hatchet | Hatchet Cloud or a self-hosted engine | tenant tokens |
| Provider (`brickie worker`) | a pod in the customer's cluster, or a developer machine | its Hatchet token, its Kubernetes service account, its store credentials |
| sandboxd | PID 1 in every sandbox | a bearer token known only to the provider |
| Sandbox TLS proxy (`brickie sandbox-tls`) | a second container in every sandbox pod (Kubernetes) | the provider's CA |
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
- On Kubernetes the hop is mutual TLS 1.3. sandboxd v1.0.8 cannot terminate TLS, so it listens on loopback only and a second container in the pod, the provider's own image running `brickie sandbox-tls`, terminates TLS on the pod IP (gRPC on 9443, REST on 8443) and forwards to loopback. The proxy accepts only client certificates signed by the provider's CA, and the provider accepts only a server certificate from that CA with the name `sandbox.brickie`, so a bearer token is never sent to anything but a sandbox pod of this provider, and nothing on the network can read it. The CA (`ca.crt`, `ca.key`) lives beside the token in `brickie-sandboxd`; the provider's client certificate is issued in memory at start and reissued before it expires. The server certificate lives in the Secret `brickie-sandbox-tls` in the sandbox namespace, mounted into the proxy container alone, never the sandbox container; it is valid 30 days and the provider reissues it a week before expiry, which the proxy picks up without a restart. Rotate everything by deleting both Secrets and restarting the provider.
- The NetworkPolicy the agent-sandbox controller keeps for every template, which the provider writes into the template, allows ingress to 9443 and 8443 only from pods matching `--provider-selector` in the provider's namespace. It holds only where the CNI enforces NetworkPolicy; the TLS holds everywhere.
- On Docker the connection is plaintext on a loopback port published to `127.0.0.1` only. The agent inside a Docker sandbox can reach sandboxd's ports, but holds only the token's digest.
- The sandboxd client never follows redirects. A sandbox that answers with one gets an error, not a request to the address it chose.
- Each turn on a sandbox is serialized in the provider, from execution through snapshot publication. Two turns never run on one sandbox at the same time, and the latest-snapshot pointer never moves backwards.
- Everything a sandbox sends back is bounded on the provider: command stdout and stderr are kept up to 1 MiB each and marked truncated beyond that, inline file reads stop at `--max-file-bytes` no matter what size the listing claimed, and a snapshot streams to the store and fails past `--max-snapshot-bytes` (1 GiB by default) without ever being held in memory.

### Sandbox to the outside

- `--sandbox-init-containers` runs operator-supplied containers in every sandbox pod before sandboxd and the sandbox container, under the pod's runtime class, for checks such as a FIPS verification script. They are provider configuration, never reachable from the SDK, and whoever sets them is trusted with every sandbox.
- A sandbox pod has no service account token (`automountServiceAccountToken: false`), no host environment variables, no privilege escalation, and the runtime default seccomp profile. On a cluster with the `kata-clh`, `gvisor` or `isolated-sandbox` (EKS Nitro) runtime class, the pod runs under it. Set `--runtime` to require one, by alias or by RuntimeClass name; `auto` falls back to the default runtime with a warning in the log.
- The sandbox needs no network for brickie itself. `files.download` and `files.upload` run on the provider, not in the sandbox. The provider fetches or sends the bytes and refuses URLs that resolve to loopback, private, link-local, or unspecified addresses, and refuses redirects that leave https. What the agent's own code may reach is `--sandbox-egress`: `internet` allows public addresses and cluster DNS and blocks private ranges and the metadata endpoint; `none` allows nothing.
- Every sandbox has a CPU, memory, and ephemeral-storage limit (`--sandbox-cpu`, `--sandbox-memory`, `--sandbox-disk`; 1, 2Gi, 10Gi by default), and the workspace volume carries the same size limit. An agent can exhaust its own sandbox, not the node. The disk limit is a ceiling, not a reservation: a sandbox pod requests 50Mi of ephemeral storage, so it schedules on any node, and the kubelet evicts it if it outgrows what the node has.
- Linux capabilities are not dropped from the sandbox container, because package managers in common images need them and the runtime class is the isolation layer. On a cluster without Kata or gVisor, add `capabilities.drop: [ALL]` to the template in `internal/driver/kubernetes` or do not run untrusted code.

### Files and snapshots

- Every file path is confined twice. The provider rejects paths that leave the workspace lexically and escapes each path segment once, so a name is always the literal name. Reads, writes, and listings then go through sandboxd, which resolves symlinks and rejects escapes with 403. `mkdir`, `rename`, `copy`, and `remove` run as commands inside the sandbox; each first resolves the nearest existing ancestor of its paths and refuses to act unless it lies under the workspace, and `remove` unlinks a symlink instead of following it. Only the agent itself, inside its own sandbox, could race that check, and it already owns everything outside the workspace there.
- A snapshot is a tar made by `tar` inside the sandbox. It is extracted inside the sandbox, never on the provider, and only its contents: the archive cannot change `/workspace` itself, which the sandbox user does not own. A hostile archive can only fill the sandbox's own workspace volume, which has a size limit.
- brickie, sandboxd and the agent-sandbox controller are FIPS 140-3 builds. The default sandbox image, `alpine:3`, is not: brickie runs no cryptography inside the sandbox image, so bring a FIPS base image when the agent's own code must use FIPS cryptography.
- The store holds snapshots, metadata, and staged files. Only the provider has its credentials. On `file://` the SDK must be on the same machine; this is for development only.
- `delete` (`$delete`) removes a sandbox and everything the store holds for it: the live container or pod first, then its metadata, so a `connect` from that moment answers "unknown sandbox" rather than ever starting an empty workspace, then its latest pointer, then its snapshots, stored results, and staged files, which all live under the sandbox's own prefix. Every step tolerates a missing object, so a retry finishes a delete that failed part way, and deleting an unknown sandbox succeeds. A fork that has not changed anything yet still points at the deleted sandbox's snapshot and can no longer `connect`; a fork that has checkpointed keeps its own copy under its own prefix and must be deleted separately. Objects left by a delete that failed every retry have no pointer and fall to the 7-day prune.
- The provider deletes its own store objects older than 7 days at start. A sandbox whose latest pointer was written within those 7 days keeps its metadata and the snapshot the pointer names, however old they are. Staged files and turn results are kept until then so a retried turn can still read its input, and a replayed read can be signed again; on S3 a one-day lifecycle rule on the staging prefix is the recommended shorter bound.

### Kubernetes permissions

The provider's Role in the sandbox namespace:

| Resource | Verbs | Why |
| --- | --- | --- |
| `sandboxclaims` | get, list, create, patch, delete | one claim per sandbox; patch extends the idle deadline |
| `sandboxtemplates`, `sandboxwarmpools` | get, create, update | one pool per image; the template carries the NetworkPolicy and resource limits |
| `secrets` | get, create, update | `brickie-sandbox-tls`, the server certificate the proxies mount |

Plus `get`, `create`, and `update` on `secrets` in the provider's own namespace for `brickie-sandboxd` (token and CA), and a ClusterRole with `get` on `runtimeclasses` to detect Kata and gVisor. Nothing on pods, events, nodes, or networkpolicies.

The agent-sandbox controller creates the pods and the NetworkPolicy. Its permissions are documented upstream.

### Lifecycle

- A sandbox expires after its idle TTL. The provider sets `spec.lifecycle.shutdownTime` on the claim to the TTL before every turn, renews it every half TTL for as long as the turn runs (restore, command, file transfer, and checkpoint included), and sets it once more after the turn, so no turn loses its pod to expiry and the idle window starts when the turn ends; the agent-sandbox controller deletes the pod when the time passes, even if the provider is down. `kill()` deletes the claim at once.
- A pod that was replaced (node drain, eviction) loses its workspace. The provider detects this through a marker file, written only after a restore succeeded and never extracted from a snapshot, and refuses to run a turn on it. `connect` restores the latest snapshot into the new pod. A fork records its inherited snapshot as its latest at creation, so it can be connected again even before its first change.
- A failure to record the latest snapshot fails the turn after three attempts, and a store that cannot be read fails a connect, so a sandbox never silently comes back empty. A turn that stored its snapshot records its result beside it, together with the latest pointer as it stood, before publishing, so a retry of the same run (Hatchet keeps the run id) returns the stored result instead of running the command a second time, and publishes its snapshot only if no later turn has published since. A store that cannot be read for that record fails the retry rather than running the command again.

## Verifying images

Every image we publish is signed with [cosign](https://docs.sigstore.dev/cosign/) keyless signing, bound to the GitHub Actions OIDC identity of the workflow run, and each platform manifest carries a syft SPDX SBOM as a signed in-toto attestation. `ghcr.io/hatchet-dev/brickie` is signed by `release.yaml` on a `v*` tag; `ghcr.io/hatchet-dev/sandboxd` and `ghcr.io/hatchet-dev/agent-sandbox-controller` are signed by a workflow run on `main`.

```sh
cosign verify ghcr.io/hatchet-dev/brickie:0.1.6 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/hatchet-dev/brickie/\.github/workflows/release\.yaml@refs/tags/v'

cosign verify ghcr.io/hatchet-dev/sandboxd:v1.0.8 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/hatchet-dev/brickie/\.github/workflows/[^@]+@refs/heads/main$'

digest=$(docker buildx imagetools inspect ghcr.io/hatchet-dev/brickie:0.1.6 --format '{{json .Manifest}}' \
  | jq -r '.manifests[] | select(.platform.architecture == "amd64") | .digest')
cosign verify-attestation --type spdxjson "ghcr.io/hatchet-dev/brickie@$digest" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/hatchet-dev/brickie/\.github/workflows/release\.yaml@refs/tags/v' \
  | jq -r .payload | base64 -d | jq .predicate > sbom.spdx.json
```

## What to check when a boundary moves

- A new way for the provider to reach a sandbox: it must carry the token and must not need pods access.
- A new place where bytes from the sandbox are processed on the provider: they must be treated as hostile.
- A new pod spec field: keep `automountServiceAccountToken: false`, `allowPrivilegeEscalation: false`, and the seccomp profile. Keep sandboxd on loopback and the certificate Secret mounted into the proxy container only.
- A new URL the provider fetches on behalf of a caller: it must go through the guarded client in `internal/driver/fsops`.
