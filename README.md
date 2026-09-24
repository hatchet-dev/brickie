# brickie

brickie runs agent sandboxes as Hatchet tasks.

A sandbox is one micro-VM. On a laptop it is a Docker Sandboxes micro-VM. On Kubernetes it is one pod under gVisor or Kata Containers. A Hatchet durable task creates the sandbox and is the agent session. Each agent turn runs in the sandbox as a Hatchet task. Each turn makes a snapshot of the workspace. A snapshot does not change. Any snapshot can start a new sandbox. A session can continue or fork from any turn. The files and the conversation are in the snapshot.

The TypeScript SDK only triggers turns. The `brickie` provider (Go) runs them, in Docker Sandboxes or in Kubernetes with [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox).

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/brickie/` | CLI. `dev` sets up local sandboxes. Hidden commands: `worker` (the provider), `csi-node`, `injector`, `bootstrap-kind`, `bootstrap-aws`. |
| `internal/engine/` | Turn contract, provider task, snapshot store, transcript. |
| `internal/driver/local/` | Docker Sandboxes driver. Uses the `sbx` CLI. |
| `internal/driver/kubernetes/` | Kubernetes driver. One agent-sandbox `SandboxClaim` per session, pod exec for turns. |
| `internal/overlay/`, `internal/csi/` | overlayfs workspaces and the CSI node plugin that mounts them. |
| `internal/awsboot/` | EKS bootstrap: cluster, nodes, ECR, S3, gVisor and Kata installers. |
| `sdks/typescript/` | The SDK. `examples/typescript/` has the demo. |
| `helm/brickie/` | Helm chart for the provider. Includes the agent-sandbox chart as a subchart. |

## Requirements

- Docker Desktop on macOS. Docker Sandboxes is part of Docker Desktop.
- Node and pnpm.
- Homebrew. The setup script uses Homebrew to install the `sbx` CLI.
- For Kubernetes: `kind`, `kubectl`, `helm`. For AWS: Docker and AWS credentials.

## Run the demo

```bash
hack/run-demo.sh
```

The script does the setup step by step. Before each step, it shows the command and asks for approval. It installs and configures Docker Sandboxes, starts Hatchet with Docker Compose, makes a token in `.env`, builds the provider in a Go container, starts the provider, and starts the demo. The script is safe to run again. It skips the steps that are complete.

The demo is one durable task with the name `agent-session`. The task creates a sandbox. The sandbox stays alive for the full session. Each message runs as one turn in that sandbox:

- Type text to send a prompt to Claude. Claude keeps the conversation between turns.
- Type `!` and a command to run a shell command.
- Type `/restore` and a snapshot ID to start a new sandbox from that snapshot.
- Type `/exit` to end the session.

After each turn, the demo shows the snapshot ID and the time for each step. Open [http://localhost:8888](http://localhost:8888) to see the session in the Hatchet dashboard. Log in with `admin@example.com` and `Admin123!!`. The demo code is in `examples/typescript/demo.ts`.

## TypeScript SDK

```ts
import '@hatchet/sandbox';

const session = hatchet.durableTask({
  name: 'agent-session',
  executionTimeout: '1h',
  fn: async (input, ctx) => {
    const sandbox = await hatchet.sandbox.create(ctx);
    await sandbox.prompt('create haiku.txt with a haiku');
    const haiku = await sandbox.exec('cat haiku.txt');

    const fork = await hatchet.sandbox.create(ctx, { from: haiku.snapshotId });
    await fork.prompt('make it darker');

    await sandbox.kill();
    await fork.kill();
    return { haiku: haiku.stdout };
  },
});
```

### Create a sandbox

- `hatchet.sandbox.create(ctx, options)` starts a micro-VM. This is the cold start.
- `options.template` selects the image. On a laptop it is a Docker Sandboxes template. The default is `claude`. On Kubernetes it is a container image. The default is the provider's `--image`.
- `options.from` starts the sandbox from a snapshot. Two sandboxes with the same `from` are a fork.

### Use the sandbox

- `sandbox.exec(cmd)` runs a shell command. It returns `{ stdout, stderr, exitCode }`. A non-zero exit code does not cause an error.
- `sandbox.prompt(text)` runs one Claude turn. It returns `{ reply, files }`. Claude keeps the conversation between turns.
- `sandbox.kill()` removes the sandbox. The provider also removes a sandbox after 30 minutes without a turn. Set `--sandbox-idle-ttl` on the provider to change this time.

Each call is one Hatchet task. Each result also has `snapshotId` and `timings`. A turn on a sandbox that was removed, or on a pod that Kubernetes restarted, fails with a clear error. Create a new sandbox from the last snapshot to continue.

### Connect to Hatchet

Set `HATCHET_CLIENT_TOKEN` to use a Hatchet instance. The provider registered in that tenant runs the turns.

Without `HATCHET_CLIENT_TOKEN`, the example starts an embedded Hatchet engine and the provider. This needs the `brickie` binary on `PATH`, or `BRICKIE_BIN` set to its location.

## Use an external Hatchet instance

The Hatchet in Docker Compose is the default. To use Hatchet Cloud or a self-hosted instance:

1. Get a token from the tenant.
2. Put the token in `.env` as `HATCHET_CLIENT_TOKEN`. Replace the token from the script.
3. Remove the line `HATCHET_CLIENT_TLS_STRATEGY=none`. This line is only for the local Hatchet.
4. Run `hack/run-demo.sh` again.

If the shell has `HATCHET_CLIENT_TLS_ROOT_CA_FILE` set, unset it before a hosted instance is used.

## Kubernetes mode

```bash
export HATCHET_CLIENT_TOKEN=<token from the tenant>
export ANTHROPIC_API_KEY=<key>   # or CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`
brickie bootstrap-kind -n brickie
cd examples/typescript && pnpm demo
```

`bootstrap-kind` creates the kind cluster, builds the provider image, loads it, and installs the chart. The provider keeps a warm pool of pods per image. `hatchet.sandbox.create` claims one pod and keeps it for the session. Every turn runs in that pod and checkpoints the workspace. `sandbox.kill` or the idle time releases the pod. The default sandbox image is `alpine:3`. Use `--image` (`BRICKIE_SANDBOX_IMAGE`) to change it.

Run one provider replica. Turns on the same sandbox are serialized in the provider process.

## AWS mode

`brickie bootstrap-aws` goes from AWS credentials to a running install on EKS. It uses the AWS SDK and the Helm SDK. The only tool it needs is Docker, to build and push the provider image. Run it with no flags for a guided setup. Each question also has a flag:

```bash
export HATCHET_CLIENT_TOKEN=<token>
brickie bootstrap-aws
brickie bootstrap-aws --region us-east-1 --cluster-name brickie --node-count 2
brickie bootstrap-aws --region us-east-1 --cluster-name brickie --runtime kata --instance-type m5.metal
brickie bootstrap-aws --region us-east-1 --existing-cluster my-cluster
```

The create path makes IAM roles, an EKS cluster in the default VPC, a managed nodegroup, an ECR repository, and an S3 snapshot bucket. Then it builds and pushes the provider image and installs the chart with overlayfs snapshots and S3 offload. The existing path skips cluster creation and gives the IAM principal cluster admin through an EKS access entry when needed. Both paths update the kubeconfig.

## Runtimes

Sandbox pods run under a runtime class that isolates agent code from the host kernel. Two runtimes are supported:

- `gvisor`: gVisor (runsc). Works on any EC2 instance type. `bootstrap-aws` installs it with a DaemonSet and creates the `gvisor` runtime class.
- `kata`: Kata Containers with Cloud Hypervisor. Each pod is a real VM. This needs KVM, so the nodes must be bare-metal x86 instances (`m5.metal`, `c5.metal`, `m6i.metal`, `c6i.metal`). `bootstrap-aws --runtime kata` installs kata-deploy 4.2.0 with the Helm SDK and uses the `kata-clh` runtime class. `bootstrap-aws` rejects other instance types.

The provider selects the runtime at startup with `sandbox.runtime` in the chart (`BRICKIE_RUNTIME`, `--runtime`):

- `auto` (default): `kata-clh` when that runtime class exists, else `gvisor`, else the default runtime with a warning. kind keeps working unchanged.
- `gvisor` or `kata`: fail at startup when the runtime class is missing.
- `none`: never set a runtime class.

Snapshots work the same under both runtimes. The overlay chain lives on the host. The driver runs `sync` inside the sandbox before each checkpoint so buffered writes reach the workspace. Under Kata the workspace is shared into the VM with virtiofs in its default `cache=auto` mode. `cache=always` is not supported. To see the runtime, run `!uname -r` in a turn. gVisor reports `4.19.0-gvisor`. Kata reports the guest kernel.

To install a runtime on an existing cluster without `bootstrap-aws`, run `go run ./hack/apply-runtime --runtime gvisor|kata` with the kubeconfig context set.

## Worker options

`brickie worker` reads flags or environment variables:

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--driver` | `BRICKIE_DRIVER` | `local` |
| `--namespace` | `BRICKIE_SANDBOX_NAMESPACE` | `default` |
| `--image` | `BRICKIE_SANDBOX_IMAGE` | driver default |
| `--credentials` | `BRICKIE_CREDENTIALS` | none |
| `--snapshot-dir` | `BRICKIE_SNAPSHOT_DIR` | `~/.brickie/snapshots` |
| `--turn-timeout` | `BRICKIE_TURN_TIMEOUT` | `15m` |
| `--sandbox-idle-ttl` | `BRICKIE_SANDBOX_IDLE_TTL` | `30m` (must exceed the turn timeout) |
| `--context` | `BRICKIE_KUBE_CONTEXT` | current context or in-cluster |
| `--slots` | `BRICKIE_SLOTS` | `10` |
| `--object-store` | `BRICKIE_OBJECT_STORE` | none |
| `--overlay` | `BRICKIE_OVERLAY` | `false` |
| `--runtime` | `BRICKIE_RUNTIME` | `auto` |
| `--injector`, `--injector-secret`, `--injector-image` | `BRICKIE_INJECTOR*` | disabled |
| `--warm-images`, `--warm-replicas` | `BRICKIE_WARM_IMAGES`, `BRICKIE_WARM_REPLICAS` | none, `2` |

`--credentials` is a comma-separated list of environment variable names. The Kubernetes driver copies those variables from the provider process into each sandbox pod (sealed by the injector when it is enabled). The local driver ignores it. Local sandboxes get no host environment variables.

## Snapshots

On a laptop and on Kubernetes without the CSI driver, a snapshot is a gzipped tar of the workspace. The provider stores it under `--snapshot-dir` with the run ID of the turn as the name. Local snapshots older than 7 days are pruned when the provider starts. With `--object-store` (`s3://`, `gs://`, `azblob://`), snapshots go to object storage with the local directory as a cache.

On Kubernetes with the CSI driver (`csi.enabled`), a snapshot is an overlayfs layer checkpointed on the node. Each turn on a live sandbox tars the sandbox's upper directory, so the cost of a checkpoint grows with the changes made in the session. A `from` snapshot from another node needs `csi.objectStore`. Set it on every cluster with more than one node.

## Observability

Every turn streams logs and traces to Hatchet, visible on the run in the dashboard. The provider logs each phase of a turn: image resolution, claim or reuse of the sandbox, restore, execution time, and checkpoint. The worker and the SDK register Hatchet's OpenTelemetry instrumentor, so trigger spans link to worker spans. Every turn carries `brickie.sandbox`, `brickie.fn`, `brickie.image`, and `brickie.from` as run metadata. Set `BRICKIE_OTEL=false` to disable tracing.
