# brickie Terraform module

Deploys the brickie controller into a Kubernetes cluster with Helm. The controller registers as a Hatchet worker and runs sandbox turns in the cluster.

The module talks to the cluster through the Helm provider. Configure the provider for your cluster before using the module.

## Usage

```hcl
provider "helm" {
  kubernetes = {
    config_path = "~/.kube/config"
  }
}

module "brickie" {
  source = "github.com/hatchet-dev/brickie//terraform"

  hatchet_token = var.hatchet_token

  sandbox_credentials = {
    ANTHROPIC_API_KEY = var.anthropic_api_key
  }
}
```

With an existing token Secret and the CSI driver enabled:

```hcl
module "brickie" {
  source = "github.com/hatchet-dev/brickie//terraform"

  hatchet_existing_secret     = "hatchet-token"
  existing_credentials_secret = "brickie-secrets"
  credential_names            = ["ANTHROPIC_API_KEY"]

  csi_enabled      = true
  csi_object_store = "s3://my-bucket/brickie"

  sandbox_image = "node:22-slim"
}
```

## Inputs

| Name | Description | Type | Default |
| --- | --- | --- | --- |
| release_name | Helm release name | `string` | `"brickie"` |
| namespace | Install namespace | `string` | `"brickie"` |
| create_namespace | Create the namespace if missing | `bool` | `true` |
| chart | Chart reference; defaults to the bundled chart | `string` | `""` |
| chart_repository | Chart repository URL | `string` | `""` |
| chart_version | Chart version constraint | `string` | `null` |
| hatchet_token | Hatchet client token (sensitive) | `string` | `""` |
| hatchet_existing_secret | Existing Secret with HATCHET_CLIENT_TOKEN | `string` | `""` |
| image_repository | Controller image repository | `string` | `"brickie-worker"` |
| image_tag | Controller image tag | `string` | `"latest"` |
| sandbox_image | Default sandbox pod image | `string` | `"alpine:3"` |
| sandbox_namespace | Namespace for sandbox pods | `string` | release namespace |
| turn_timeout | Timeout per sandbox turn | `string` | `"15m"` |
| sandbox_credentials | Env vars injected into sandboxes (sensitive) | `map(string)` | `{}` |
| existing_credentials_secret | Existing Secret for sandbox credentials | `string` | `""` |
| credential_names | Keys to inject from the existing Secret | `list(string)` | `[]` |
| agent_sandbox_enabled | Install the agent-sandbox subchart | `bool` | `true` |
| csi_enabled | Deploy the CSI node DaemonSet | `bool` | `false` |
| csi_object_store | Object store URL for snapshot layers | `string` | `""` |
| replica_count | Controller replicas | `number` | `1` |
| values | Extra Helm values as YAML strings | `list(string)` | `[]` |
| helm_timeout | Helm operation timeout in seconds | `number` | `300` |
| atomic | Roll back on failed install or upgrade | `bool` | `true` |

Exactly one of `hatchet_token` or `hatchet_existing_secret` must be set.

## Outputs

| Name | Description |
| --- | --- |
| release_name | Name of the Helm release |
| namespace | Namespace the controller is installed into |
| sandbox_namespace | Namespace sandbox pods run in |
| chart_version | Deployed chart version |

## Object store credentials

The CSI driver reads object store credentials from the ambient environment: IRSA on EKS, Workload Identity on GKE and AKS, or static keys in a Secret. This module does not create cloud IAM resources. Pair it with your cloud's IAM module and bind the role to the CSI DaemonSet service account.
