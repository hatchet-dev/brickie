# Configuration

Every flag of brickie worker and its environment variable.

`brickie worker --help` prints the same list. Every flag has a `BRICKIE_*` environment variable, which the Helm chart sets for you. `HATCHET_CLIENT_TOKEN` is required and has no flag.

## Provider

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `--driver` | `BRICKIE_DRIVER` | `docker` | Where sandboxes run, `docker` or `kubernetes`. |
| `--image` | `BRICKIE_SANDBOX_IMAGE` | `alpine:3` | Default sandbox image. Needs `sh` and `tar`. |
| `--sandboxd-image` | `BRICKIE_SANDBOXD_IMAGE` | `ghcr.io/hatchet-dev/sandboxd:v1.0.8` | Image the `sandboxd` binary is taken from. Must match the agent-sandbox controller version. |
| `--store` | `BRICKIE_STORE` | `file://$HOME/.brickie/store` | Bucket for snapshots, metadata and large files, see below. |
| `--max-file-bytes` | `BRICKIE_MAX_FILE_BYTES` | `2097152` | Largest file carried inside a Hatchet task. Larger files go through the store. Hatchet must allow the base64 payload, `SERVER_GRPC_MAX_MSG_SIZE` defaults to 4 MiB. |
| `--max-snapshot-bytes` | `BRICKIE_MAX_SNAPSHOT_BYTES` | `1073741824` | Largest workspace snapshot a turn may produce. |
| `--sandbox-idle-ttl` | `BRICKIE_SANDBOX_IDLE_TTL` | `15m` | Destroy a sandbox after this long without a call. The chart sets `30m`. |
| `--max-sandbox-idle-ttl` | `BRICKIE_MAX_SANDBOX_IDLE_TTL` | `24h` | Longest idle TTL a sandbox may ask for at create. |
| `--slots` | `BRICKIE_SLOTS` | `10` | Sandbox calls the provider runs at the same time. |
| `--log-format` | `BRICKIE_LOG_FORMAT` | `json` | `json` or `text`. Everything goes to stdout. |

A sandbox's idle TTL is a provider setting. The duration of one call is bounded by the SDK's `timeoutMs` and the calling task's own timeout, never by a provider flag.

## Kubernetes driver

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `--namespace` | `BRICKIE_SANDBOX_NAMESPACE` | `default` | Namespace for sandbox pods and claims. |
| `--runtime` | `BRICKIE_SANDBOX_RUNTIME` | `auto` | `auto`, `kata`, `gvisor`, `nitro`, `none` or any RuntimeClass name. The chart sets `kata`. |
| `--warm-replicas` | `BRICKIE_WARM_REPLICAS` | `2` | Ready pods kept per sandbox image. |
| `--provider-namespace` | `BRICKIE_PROVIDER_NAMESPACE` | the pod's own | Namespace of the provider pod and its `brickie-sandboxd` Secret. |
| `--provider-selector` | `BRICKIE_PROVIDER_SELECTOR` | `app.kubernetes.io/name=brickie` | Labels of the provider pod. Only pods matching them may reach sandboxes. |
| `--provider-image` | `BRICKIE_PROVIDER_IMAGE` | | The provider's own image, run in every sandbox pod as the TLS proxy. Required. |
| `--sandbox-egress` | `BRICKIE_SANDBOX_EGRESS` | `internet` | `internet` allows public addresses and cluster DNS, `none` allows nothing. |
| `--sandbox-node-selector` | `BRICKIE_SANDBOX_NODE_SELECTOR` | | Node labels for sandbox pods, `key=value` pairs separated by commas. |
| `--sandbox-tolerations` | `BRICKIE_SANDBOX_TOLERATIONS` | | Tolerations for sandbox pods as a JSON list. |
| `--sandbox-init-containers` | `BRICKIE_SANDBOX_INIT_CONTAINERS` | | Init containers run in every sandbox pod before the sandbox starts, as a JSON list of Kubernetes containers. The names `sandboxd`, `sandbox` and `tls` are reserved. |
| `--sandbox-cpu` | `BRICKIE_SANDBOX_CPU` | `1` | CPU limit per sandbox. |
| `--sandbox-memory` | `BRICKIE_SANDBOX_MEMORY` | `2Gi` | Memory limit per sandbox. |
| `--sandbox-disk` | `BRICKIE_SANDBOX_DISK` | `10Gi` | Ephemeral storage limit per sandbox, including the workspace. |

## Store

The store is one bucket opened through [gocloud.dev](https://gocloud.dev/howto/blob/). A prefix goes in the query, not in the path.

| URL | Backend |
| --- | --- |
| `s3://bucket?prefix=brickie/&region=us-east-2` | Amazon S3. Credentials from the environment or workload identity. |
| `s3://bucket?prefix=brickie/&endpoint=https://host&use_path_style=true` | Any S3-compatible store. |
| `gs://bucket?prefix=brickie/` | Google Cloud Storage. |
| `azblob://container?prefix=brickie/` | Azure Blob Storage. |
| `file:///var/lib/brickie` | A directory. One machine only, the SDK must run on the same host for large files. |
