# Security

Sandboxes run untrusted code. While this document lays out the security boundaries when using `brickie`, we highly recommend running sandboxes in a completely isolated cloud account, or at minimum, a separate VPC with tightly controlled IAM permissions.

To report a vulnerability, use the [security policy](../SECURITY.md), not a public issue.

## Who trusts what

| Component | Runs where                                   | Trusts                                                        |
| --------- | -------------------------------------------- | ------------------------------------------------------------- |
| SDK       | your application                             | its Hatchet tenant token                                      |
| Hatchet   | Hatchet Cloud or self-hosted                 | tenant tokens                                                 |
| Provider  | a pod in your cluster, or your laptop        | its Hatchet token, its service account, its store credentials |
| sandboxd  | PID 1 in every sandbox                       | a bearer token only the provider knows                        |
| TLS proxy | a sidecar in every sandbox pod on Kubernetes | the provider's CA                                             |
| Sandbox   | a pod, or a container on Docker              | nothing                                                       |

## SDK to Hatchet

The SDK only has a tenant token. It never talks directly to the provider, the store or a sandbox. Every call is a `brickie-sandbox` task run in that tenant.

Sandbox IDs must match `^[a-z0-9][a-z0-9-]{0,62}$`. Large files move over presigned URLs that are good for one object, one sandbox and 15 minutes.

## Hatchet to provider

One provider serves one tenant. Never share a provider between tenants - the store and the sandbox namespace are not split by tenant.

The provider checks the sandbox ID and every path it is given. It does not check the image. If a tenant must not pull arbitrary images, enforce that on the cluster with an admission policy.

## Provider to sandbox

The provider only ever talks to `sandboxd` inside the sandbox. It never uses `pods/exec` and needs no access to pods.

Every request carries a bearer token. `sandboxd` only knows the SHA-256 of it, so it can check requests but never reuse the token. The token sits in the Secret `brickie-sandboxd` in the provider's namespace, never in the sandbox namespace.

On Kubernetes the hop is mutual TLS 1.3. `sandboxd` listens on loopback only. A sidecar, the provider's own image running `brickie sandbox-tls`, terminates TLS on the pod IP and only accepts client certificates from the provider's CA. The provider only accepts a server certificate from that CA named `sandbox.brickie`. So the token can only ever reach a sandbox of this provider, and nothing on the wire can read it. A `NetworkPolicy` also limits those ports to pods matching `--provider-selector`. Certificates rotate on their own.

To rotate everything by hand, delete the two Secrets, `brickie-sandboxd` and `brickie-sandbox-tls`, delete the warm pools, and restart the provider.

Whatever a sandbox sends back is capped. `stdout` and `stderr` stop at 1 MiB each. A file read stops at `--max-file-bytes` no matter what the sandbox claims the size is. A snapshot is streamed to the store and fails past `--max-snapshot-bytes`. Redirects from a sandbox are never followed.

## Sandbox to the outside

A sandbox pod has no service account token, no host environment variables, no privilege escalation and the default seccomp profile. It runs under the RuntimeClass `--runtime` names, `kata-clh`, `gvisor` or `isolated-sandbox`, which is the real isolation layer. Linux capabilities are not dropped because package managers in common images need them.

The sandbox needs no network of its own. `files.download` and `files.upload` run on the provider, which refuses private, loopback, link-local and unspecified addresses and any redirect off https. What untrusted code may reach is `--sandbox-egress`. `internet` allows public addresses and cluster DNS, `none` allows nothing. Both are a `NetworkPolicy` that the provider writes into the sandbox template, see [`networkPolicy()`](../internal/driver/kubernetes/kubernetes.go) in the Kubernetes driver. The Helm chart can add a second one that opens extra CIDRs through `networkPolicy.egressCIDRs`, see [`networkpolicy.yaml`](https://github.com/hatchet-dev/hatchet-charts/blob/main/charts/brickie/templates/networkpolicy.yaml) in the chart.

Every sandbox has CPU, memory and disk limits, `--sandbox-cpu`, `--sandbox-memory`, `--sandbox-disk`. The disk limit is also the size of the workspace. Specifically, a sandbox can fill itself up and not the node.

`--sandbox-init-containers` run before anything else in the pod, under the same RuntimeClass. Use them for checks that must pass before untrusted code runs, such as a FIPS check. Whoever configures them is trusted with every sandbox.

## Files and snapshots

Every path is checked twice, once by the provider, once by `sandboxd`, which also resolves symlinks. `mkdir`, `rename`, `copy` and `remove` run as commands inside the sandbox and refuse to act outside `/workspace`.

A snapshot is a tarball archive made inside the sandbox and unpacked inside the sandbox, never on the provider. Specifically, a hostile archive can only fill the sandbox's own workspace.

`delete` removes the running sandbox first, then its metadata, then everything else. From the moment the metadata is gone, `connect` says "unknown sandbox" rather than ever starting empty. The provider also prunes store objects older than 7 days at start, except the metadata and latest snapshot of any sandbox used within those 7 days.

## Kubernetes permissions

The provider's `Role` in the sandbox namespace has the following:

| Resource                               | Verbs                            | Why                        |
| -------------------------------------- | -------------------------------- | -------------------------- |
| `sandboxclaims`                        | get, list, create, patch, delete | one claim per sandbox      |
| `sandboxtemplates`, `sandboxwarmpools` | get, create, update              | one pool per image         |
| `secrets`                              | get, create, update              | the TLS server certificate |

Plus the same three verbs on `secrets` in its own namespace, and `get` on `runtimeclasses` cluster-wide. The `agent-sandbox` controller creates the pods and the `NetworkPolicy`.

## Lifecycle

The provider sets the sandbox's expiry on the claim before every turn, renews it while the turn runs, and sets it again after. The agent-sandbox controller removes the pod when the time passes, even if the provider is down.

A pod that was replaced by a drain or eviction has lost its workspace. The provider notices through a marker file that is never part of a snapshot, refuses to run a turn on it, and restores the latest snapshot on the next `connect`. If the store cannot be read, a connect fails rather than starting empty.

## Verifying images

Every image we publish is signed with [cosign](https://docs.sigstore.dev/cosign/) keyless signing and carries a syft SPDX SBOM. `ghcr.io/hatchet-dev/brickie` is signed by `release.yaml` on a `v*` tag. `sandboxd` and `agent-sandbox-controller` are signed by a workflow run on `main`.

```sh
cosign verify ghcr.io/hatchet-dev/brickie:0.1.7 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/hatchet-dev/brickie/\.github/workflows/release\.yaml@refs/tags/v'

cosign verify ghcr.io/hatchet-dev/sandboxd:v1.0.8 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/hatchet-dev/brickie/\.github/workflows/[^@]+@refs/heads/main$'

digest=$(docker buildx imagetools inspect ghcr.io/hatchet-dev/brickie:0.1.7 --format '{{json .Manifest}}' \
  | jq -r '.manifests[] | select(.platform.architecture == "amd64") | .digest')
cosign verify-attestation --type spdxjson "ghcr.io/hatchet-dev/brickie@$digest" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/hatchet-dev/brickie/\.github/workflows/release\.yaml@refs/tags/v' \
  | jq -r .payload | base64 -d | jq .predicate > sbom.spdx.json
```
