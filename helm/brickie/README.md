# Brickie Helm chart

One chart installs everything brickie needs on a cluster:

- the brickie provider, a Deployment that connects to Hatchet as a gRPC operator;
- agent-sandbox v1.0.7 (kubernetes-sigs), the controller for sandbox pods and warm pools, as a bundled subchart;
- Kata Containers with Cloud Hypervisor, the default sandbox runtime, as a bundled kata-deploy subchart;
- gVisor, an optional alternative runtime, as a DaemonSet installer;
- a NetworkPolicy that isolates sandbox pods.

No external chart repositories are needed. All subcharts are vendored under `charts/`.

## Install

```bash
helm upgrade --install brickie ./helm/brickie \
  --namespace brickie --create-namespace \
  --set hatchet.token=$HATCHET_CLIENT_TOKEN \
  --set objectStore=s3://my-bucket/brickie \
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

To use gVisor instead: `--set sandbox.runtime=gvisor --set gvisor.install=true --set kata-deploy.enabled=false`. gVisor runs on any instance type. Its installer is also a privileged DaemonSet.

If your cluster already provides a runtime class, disable both installers and set `sandbox.runtime` to `kata`, `gvisor`, or `none`. The provider looks for the classes `kata-clh` and `gvisor`.

### 3. An object store

Sandbox snapshots and files larger than 2 MiB go to a bucket. The provider pod has no durable disk, so without a bucket snapshots are lost on restart and `connect` cannot resume a sandbox.

- Create a bucket. Set `objectStore` to `s3://bucket/prefix`. S3-compatible stores: `s3://bucket?endpoint=https://host&use_path_style=true&region=us-east-1`.
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

- Alternatively, put static credentials (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`) in a Secret and pass its name as `objectStoreSecret`.
- Add a lifecycle rule that expires the `brickie/brickie-staging/` prefix after one day. Staged files are normally deleted by the provider; the rule catches the rest.
- Your application also reaches the bucket for files over 2 MiB, by presigned URLs the provider mints. It needs network access to the bucket endpoint but no credentials.

### 4. A CNI that enforces NetworkPolicy

The chart installs a NetworkPolicy that gives sandbox pods no ingress and no egress except DNS (`networkPolicy.allowDNS`) and the CIDRs in `networkPolicy.egressCIDRs` (for example your LLM provider's ranges). It only takes effect if the CNI enforces policies:

- **EKS:** the default VPC CNI enforces policies only when network policy is enabled on the add-on (`enableNetworkPolicy: true`), or use Cilium or Calico.
- **kind and most managed clusters with Cilium or Calico:** enforced by default.

The provider is not affected by this policy: it reaches sandbox pods through the Kubernetes API, not the pod network.

### 5. Images

WIP

<!-- - The provider image: `ghcr.io/hatchet-dev/brickie`. For a private registry, build from the repository `Dockerfile`, push, and set `image.repository` and `image.tag`.
- Sandbox images: any image with `sh` and `tar`. Set the default with `sandbox.image`; the SDK can pick another per sandbox. Pull secrets go in `imagePullSecrets`, which the sandbox namespace's default ServiceAccount must also carry.
- The nodes pull `quay.io/kata-containers/kata-deploy` (Kata) or `alpine` plus `storage.googleapis.com/gvisor` (gVisor) during install. -->

### 6. Namespaces

The provider runs in the release namespace. Sandbox pods run there too by default; set `sandbox.namespace` to put them in a separate namespace that your policies and quotas can target. The chart creates the Role and RoleBinding in that namespace.

## What the chart creates

| Resource | Namespace | Purpose |
| --- | --- | --- |
| Deployment `brickie-brickie` | release | The provider, one replica |
| ServiceAccount `brickie-brickie` | release | Identity of the provider; carries the IRSA annotation |
| Role and RoleBinding | sandbox | Create, get, list, watch, update, patch, delete on `sandboxclaims`, `sandboxtemplates`, `sandboxwarmpools`; get, list, watch on `pods`, `events`, `sandboxes`; create on `pods/exec` |
| ClusterRole and binding | cluster | Get on `runtimeclasses`, to detect the runtime at startup |
| Secret `brickie-brickie` | release | The Hatchet token and `sandbox.credentials`, when given inline |
| NetworkPolicy `brickie-brickie-sandboxes` | sandbox | Default deny for sandbox pods |
| RuntimeClass `kata-clh` + kata-deploy DaemonSet | release | Kata runtime on every selected node |
| RuntimeClass `gvisor` + installer DaemonSet | release | gVisor runtime (release pinned in `gvisor.release`), when `gvisor.install` is true |
| agent-sandbox controller and CRDs (v1.0.7) | `agent-sandbox-system` | Sandbox pods, warm pools, claims, idle expiry |

The provider never needs cluster-admin. It cannot read Secrets, create arbitrary pods, or exec into pods outside the sandbox namespace.

## Values

| Value | Default | Meaning |
| --- | --- | --- |
| `hatchet.token` / `hatchet.existingSecret` | required | Hatchet client token |
| `image.repository`, `image.tag` | WIP | Provider image |
| `serviceAccount.annotations` | `{}` | For example the IRSA role ARN |
| `sandbox.namespace` | release namespace | Where sandbox pods run |
| `sandbox.image` | `alpine:3` | Default sandbox image |
| `sandbox.runtime` | `kata` | `kata`, `gvisor`, `auto`, or `none` |
| `sandbox.idleTTL` | `30m` | Remove a sandbox after this long without a call |
| `sandbox.maxIdleTTL` | `24h` | Largest `idleTimeoutMs` a create may ask for |
| `sandbox.maxFileBytes` | `2097152` | Largest file carried inside a Hatchet task; larger files use the object store |
| `sandbox.credentials` | `{}` | Environment variables copied into every sandbox pod |
| `sandbox.warmImages`, `sandbox.warmReplicas` | `[]`, `2` | Images to pre-warm at start, and warm pods per image |
| `objectStore`, `objectStoreSecret` | none | Bucket URL and, if not using workload identity, a Secret with its credentials |
| `networkPolicy.enabled`, `allowDNS`, `egressCIDRs` | `true`, `true`, `[]` | Sandbox pod isolation |
| `kata-deploy.*` | Cloud Hypervisor only, DaemonSet mode | Passed to the bundled kata-deploy chart |
| `gvisor.install`, `gvisor.release` | `false`, `20261005.0` | gVisor installer |
| `agent-sandbox.enabled` | `true` | Set to `false` if agent-sandbox is already on the cluster |
| `slots` | `10` | Concurrent turns the provider accepts |

`values.schema.json` rejects unknown keys, so a typo fails at install time.
