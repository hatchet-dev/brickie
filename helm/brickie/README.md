# Brickie Helm chart

One chart installs everything brickie needs on a cluster:

- the brickie provider, a Deployment that connects to Hatchet as a gRPC operator;
- agent-sandbox v1.0.8 (kubernetes-sigs), the controller for sandbox pods and warm pools, as a bundled subchart;
- Kata Containers with Cloud Hypervisor, the default sandbox runtime, as a bundled kata-deploy subchart;
- gVisor, an optional alternative runtime, as a DaemonSet installer;
- a NetworkPolicy that isolates sandbox pods.

No external chart repositories are needed. All subcharts are vendored under `charts/`.

## Install

```bash
helm upgrade --install brickie ./helm/brickie \
  --namespace brickie --create-namespace \
  --set hatchet.token=$HATCHET_CLIENT_TOKEN \
  --set store=s3://my-bucket/brickie \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/brickie-provider
```

Then use the SDK with the same `HATCHET_CLIENT_TOKEN`. The provider in the cluster runs the turns.

## What you set up before the install

### 1. A Hatchet tenant with gRPC operators

- Hatchet v0.110 or newer, with `SERVER_GRPC_OPERATORS_ENABLED=true` on the engine if self-hosting.
- A client token for the tenant. Pass it as `hatchet.token`, or create a Secret with the key `HATCHET_CLIENT_TOKEN` and pass its name as `hatchet.existingSecret`.
- The provider makes one outbound gRPC connection to Hatchet. Allow egress from the provider pod to the Hatchet gRPC address. Nothing inbound is needed.

### 2. Nodes that can run the sandbox runtime

The chart defaults to Kata Containers (`sandbox.runtime: kata`). Kata runs each sandbox in a micro-VM and needs KVM on the node:

- **EKS:** a managed nodegroup of bare-metal x86 instances (`m5.metal`, `c5.metal`, `m6i.metal`, `c6i.metal`). Cloud Hypervisor is amd64-only, so no Graviton.
- The kata-deploy DaemonSet is privileged, writes the Kata runtime to each node, and configures containerd. Use `kata-deploy.nodeSelector` to limit it to the sandbox nodes.

To use gVisor instead: `--set sandbox.runtime=gvisor --set gvisor.install=true --set kata-deploy.enabled=false`. gVisor runs on any instance type. Its installer is also a privileged DaemonSet; `gvisor.nodeSelector` limits it to the sandbox nodes and makes the `gvisor` RuntimeClass schedule only there.

If your cluster already provides a runtime class, disable both installers and set `sandbox.runtime` to `kata`, `gvisor`, or `none`. The provider looks for the classes `kata-clh` and `gvisor`.

### 3. An object store

Sandbox snapshots, sandbox metadata, and files larger than 2 MiB go to a bucket. Without one the provider keeps them on its pod's emptyDir: snapshots are lost on restart, `connect` cannot resume a sandbox, and files over 2 MiB fail because your application cannot reach the pod's disk. Development only.

- Create a bucket. Set `store` to `s3://bucket/prefix`. S3-compatible stores: `s3://bucket?endpoint=https://host&use_path_style=true&region=us-east-1`.
- Give the provider access. On EKS, create an IAM role with this policy and annotate the ServiceAccount with its ARN through `serviceAccount.annotations` (IRSA). The role needs the `sts:AssumeRoleWithWebIdentity` trust for the cluster's OIDC provider and the ServiceAccount `system:serviceaccount:<namespace>:brickie-brickie`.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": ["s3:ListBucket"], "Resource": "arn:aws:s3:::my-bucket" },
    { "Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"], "Resource": "arn:aws:s3:::my-bucket/brickie/*" }
  ]
}
```

- Alternatively, put static credentials (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`) in a Secret and pass its name as `storeSecret`.
- Add a lifecycle rule that expires the `brickie/staging/` prefix after one day. Staged files are normally deleted by the provider; the rule catches the rest. The provider also removes everything under the prefix older than seven days at each start.
- Your application also reaches the bucket for files over 2 MiB, by presigned URLs the provider mints. It needs network access to the bucket endpoint but no credentials.

### 4. A CNI that enforces NetworkPolicy

The chart installs a NetworkPolicy that gives sandbox pods no egress except DNS (`networkPolicy.allowDNS`) and the CIDRs in `networkPolicy.egressCIDRs` (for example your LLM provider's ranges), and no ingress except from the provider pod on ports 8080 and 9090, the sandbox daemon's file and command ports. It only takes effect if the CNI enforces policies:

- **EKS:** the default VPC CNI enforces policies only when network policy is enabled on the add-on (`enableNetworkPolicy: true`), or use Cilium or Calico.
- **kind and most managed clusters with Cilium or Calico:** enforced by default.

Without an enforcing CNI, any pod on the cluster can reach a sandbox daemon's ports. The daemon still requires the bearer token the provider holds, so this is a second layer, not the only one. See `docs/SECURITY.md`.

### 5. Images

- The provider image: `ghcr.io/hatchet-dev/brickie`, tagged with each release; the chart defaults to its own `appVersion`. While the GitHub repository is private the package is private too, so give the provider a pull secret made from a GitHub token with `read:packages` and list it in `imagePullSecrets`. For a private registry, build from the repository `Dockerfile`, push, and set `image.repository` and `image.tag`.
- Sandbox images: any image with `sh` and `tar`. Set the default with `sandbox.image`; the SDK can pick another per sandbox. Sandbox pods are pulled by the sandbox namespace's default ServiceAccount, so give that one its own pull secrets. Every sandbox pod also pulls the pinned `sandboxd` image from `us-central1-docker.pkg.dev/k8s-staging-images`.
- The nodes pull `quay.io/kata-containers/kata-deploy` (Kata) or `alpine` plus `storage.googleapis.com/gvisor` (gVisor) during install.

### 6. Namespaces

The provider runs in the release namespace. Sandbox pods run there too by default; set `sandbox.namespace` to put them in a separate namespace that your policies and quotas can target. The chart creates the Role and RoleBinding in that namespace.

## What the chart creates

| Resource | Namespace | Purpose |
| --- | --- | --- |
| Deployment `brickie-brickie` | release | The provider, one replica |
| ServiceAccount `brickie-brickie` | release | Identity of the provider; carries the IRSA annotation |
| Role and RoleBinding | sandbox | Get, list, create, patch, delete on `sandboxclaims`; get, create, update on `sandboxtemplates` and `sandboxwarmpools`; create on `secrets` and get on the one Secret `brickie-sandboxd` |
| ClusterRole and binding | cluster | Get on `runtimeclasses`, to detect the runtime at startup |
| Secret `brickie-brickie` | release | The Hatchet token, when given inline |
| Secret `brickie-sandboxd` | sandbox | Created by the provider at first start: the bearer token every sandbox daemon requires |
| NetworkPolicy `brickie-brickie-sandboxes` | sandbox | Default deny for sandbox pods, with ingress from the provider only |
| RuntimeClass `kata-clh` + kata-deploy DaemonSet | release | Kata runtime on every selected node |
| RuntimeClass `gvisor` + installer DaemonSet | release | gVisor runtime (release pinned in `gvisor.release`), when `gvisor.install` is true |
| agent-sandbox controller and CRDs (v1.0.8) | `agent-sandbox-system` | Sandbox pods, warm pools, claims, idle expiry |

The provider never needs cluster-admin. It cannot create pods, exec into pods, or read any Secret other than its own sandbox daemon token.

## Values

| Value | Default | Meaning |
| --- | --- | --- |
| `hatchet.token` / `hatchet.existingSecret` | required | Hatchet client token |
| `image.repository`, `image.tag` | `ghcr.io/hatchet-dev/brickie`, chart `appVersion` | Provider image |
| `serviceAccount.annotations` | `{}` | For example the IRSA role ARN |
| `sandbox.namespace` | release namespace | Where sandbox pods run |
| `sandbox.image` | `alpine:3` | Default sandbox image |
| `sandbox.runtime` | `kata` | `kata`, `gvisor`, `auto`, or `none` |
| `sandbox.idleTTL` | `30m` | Remove a sandbox after this long without a call |
| `sandbox.maxIdleTTL` | `24h` | Largest `idleTimeoutMs` a create may ask for |
| `sandbox.maxFileBytes` | `2097152` | Largest file carried inside a Hatchet task; larger files use the object store |
| `sandbox.warmReplicas` | `2` | Ready pods each warm pool keeps |
| `store`, `storeSecret` | none | Bucket URL and, if not using workload identity, a Secret with its credentials |
| `networkPolicy.enabled`, `allowDNS`, `egressCIDRs` | `true`, `true`, `[]` | Sandbox pod isolation |
| `kata-deploy.*` | Cloud Hypervisor only, DaemonSet mode | Passed to the bundled kata-deploy chart |
| `gvisor.install`, `gvisor.release`, `gvisor.nodeSelector` | `false`, `20261005.0`, `{}` | gVisor installer and where it runs |
| `agent-sandbox.enabled` | `true` | Set to `false` if agent-sandbox is already on the cluster |
| `slots` | `10` | Concurrent turns the provider accepts |

`values.schema.json` rejects unknown keys, so a typo fails at install time.
