# brickie

Universal sandbox API and session manager. Deploys into your own cloud account.

brickie runs agent sandboxes as Hatchet durable tasks. Each sandbox turn is one task. When a turn finishes, the workspace is saved as a snapshot. Later turns can resume from a snapshot or fork it.

The SDKs only trigger turns. The `brickie` engine (Go) executes them, either locally in Docker Sandboxes or in Kubernetes using [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox).

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/brickie/` | CLI. `dev` sets up local sandboxes on this machine. Hidden commands: `worker` (the sandbox provider), `bootstrap-kind`, `bootstrap-aws`. |
| `internal/engine/` | Durable task definition, snapshot store, turn contract, node runner. |
| `internal/driver/local/` | Docker Sandboxes driver. Uses the `sbx` CLI. Docker does not publish a Go client. |
| `internal/driver/kubernetes/` | Kubernetes driver. Creates agent-sandbox `Sandbox` resources with the official v1beta1 types and runs commands with pod exec. |
| `sdks/typescript`, `sdks/python`, `sdks/go` | Client SDKs. |
| `examples/typescript`, `examples/python`, `examples/go` | Runnable sessions. |
| `helm/brickie/` | Helm chart for the provider. Includes the agent-sandbox chart as a subchart. |

## Requirements

- Go 1.26 or newer to build the engine
- Docker Desktop (local sandboxes are macOS-only today; Docker Sandboxes ships with Docker Desktop on macOS)
- For local sandboxes: Homebrew (installs the `sbx` CLI)
- For Kubernetes: `kind`, `kubectl`, `helm`
- Node and pnpm for the TypeScript SDK and examples

## Try it locally

Everything runs on your machine: sandboxes in Docker Sandboxes micro-VMs, orchestration in an embedded Hatchet engine. The only external traffic is Claude API calls.

```bash
go build -o /usr/local/bin/brickie ./cmd/brickie
brickie dev
pnpm install                       # repo root — the examples use the workspace SDK
cd examples/typescript && pnpm demo
```

`brickie dev` checks Docker Desktop, installs and signs in the `sbx` CLI, initializes the sandbox network policy, and authenticates Claude inside sandboxes. Every step is skipped once it holds, so re-running it is free; sign-in steps open a browser only the first time (`--claude-auth` forces Claude re-auth after a credential change). It ends with a summary of what runs where.

`pnpm demo` is an interactive session shell: each message you type runs as one durable `agent-turn` task in a fresh sandbox, plain text goes to Claude, `!command` runs a shell command, and every turn prints its snapshot id and a latency breakdown. `/restore <id>` forks a new sandbox from any earlier snapshot. The other examples (`pnpm command`, `pnpm prompt`, `pnpm harness`) are non-interactive end-to-end runs that finish with `e2e ok`.

How placement works: when `HATCHET_CLIENT_TOKEN` is not set, the SDK starts an embedded Hatchet engine (downloaded once and cached in `~/.hatchet`, with a bundled Postgres — the first run takes a minute) and spawns `brickie worker --driver local` as a sidecar. If the `brickie` binary is not on `PATH`, set `BRICKIE_BIN` to its location. When `HATCHET_CLIENT_TOKEN` is set, the SDK starts nothing and turns are orchestrated by that Hatchet instance instead — sandboxes still run wherever a provider is running. If your shell has `HATCHET_CLIENT_TLS_ROOT_CA_FILE` set, unset it before targeting a hosted instance.

Local security model:

- Sandboxes are Docker Sandboxes micro-VMs. The per-turn workspace directory is the only path shared with the host.
- No environment variables from your machine are injected into local sandboxes (`--credentials` applies to the Kubernetes driver only). Claude authenticates via the credential stored inside the sandbox agent kit by `brickie dev`.
- Snapshots are tars of the workspace under `~/.brickie/snapshots`, pruned after 7 days.
- In local mode, `image` selects an sbx template (built from the Docker Sandboxes base image), not an arbitrary container image; on Kubernetes it is a container image. Omit it locally to use the default Claude template.

## Kubernetes mode

Kubernetes mode uses Hatchet Cloud for orchestration and a kind cluster for sandboxes.

```bash
export HATCHET_CLIENT_TOKEN=<token from your tenant>
export ANTHROPIC_API_KEY=<key>   # or CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`
brickie bootstrap-kind -n brickie
cd examples/typescript && pnpm k8s
```

`bootstrap-kind` creates the kind cluster, builds the provider image from the root `Dockerfile`, loads it into the cluster, and installs the Helm chart. If agent-sandbox is already installed on the cluster, the subchart is skipped.

When `HATCHET_CLIENT_TOKEN` is set, the SDK does not start anything. It triggers `brickie-sandbox` tasks and the provider registered in the tenant runs them. The provider claims a pod from a per-image warm pool for each turn, restores the snapshot into it when the turn continues from one, runs the turn, checkpoints the workspace, and releases the pod. The default sandbox image is `alpine:3`. Use `--image` (`BRICKIE_SANDBOX_IMAGE`) to change it.

If you develop hatchet locally with mkcert, run `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` before connecting to Hatchet Cloud. Otherwise the gRPC connection fails with a TLS error.

## Claude Managed Agents

Claude Managed Agents run the agent loop on Anthropic's orchestration layer. With a self-hosted environment, tool execution moves to infrastructure you control: a worker polls Anthropic's work queue and runs bash and file tools locally. brickie is that infrastructure. Each managed agent run executes inside a brickie sandbox as one durable Hatchet turn, so the agent's workspace lives in your cluster, is snapshotted after every run, and can be resumed or forked like any other brickie session.

Setup:

1. Create a self-hosted environment: `ant beta:environments create <<< '{"name":"brickie-byoc","config":{"type":"self_hosted"}}'`
2. Generate an environment key for it in the Claude Console, on the environment page.
3. Export `ANTHROPIC_ENVIRONMENT_ID`, `ANTHROPIC_ENVIRONMENT_KEY`, and `ANTHROPIC_API_KEY`.
4. Run the brickie provider with the environment credentials injected into sandboxes: `--credentials ANTHROPIC_ENVIRONMENT_KEY,ANTHROPIC_ENVIRONMENT_ID`, or `sandbox.credentials` in the Helm chart.

```bash
cd examples/typescript && pnpm managed-agent
```

The example creates a managed agent once (reuse it with `BRICKIE_MANAGED_AGENT_ID`), starts a session, and drives two turns. For each turn it sends a user message, then runs a `managed-agent-turn` durable task whose sandbox function drains the environment work queue and services the run with the workspace as the tool working directory. The second turn restores the first turn's snapshot, so the agent sees the files it wrote earlier. Give the example a dedicated environment: a turn services whatever work is queued on the environment, so sessions from elsewhere would be picked up too.

## AWS mode

`brickie bootstrap-aws` takes you from AWS credentials to a running brickie install on EKS. It talks to AWS through the SDK and installs the chart through the Helm SDK, so the only tool it needs is Docker, for building and pushing the provider image. No eksctl, kubectl, or helm binaries are required. Run it interactively and it asks for the region, whether to create a new cluster or reuse an existing one, and the node shape. Every question also has a flag, so CI can run it non-interactively:

```bash
export HATCHET_CLIENT_TOKEN=<token>
brickie bootstrap-aws                      # interactive
brickie bootstrap-aws --region us-east-1 --cluster-name brickie --node-count 2
brickie bootstrap-aws --region us-east-1 --existing-cluster my-cluster
```

The create path provisions IAM roles, an EKS cluster in the default VPC, a managed nodegroup, an ECR repository, and an S3 snapshot bucket, then builds and pushes the provider image and installs the chart with overlayfs and S3 snapshots enabled. The existing path skips cluster creation and grants your IAM principal cluster admin through an EKS access entry when needed. Both paths update your kubeconfig with an `aws eks get-token` entry.

## gVisor

bootstrap-aws installs gVisor (runsc) on every node through a DaemonSet and creates a `gvisor` RuntimeClass. The provider detects that runtime class at startup and runs every sandbox pod under gVisor, so agent code is isolated from the host kernel. Detection is controlled by `gvisor.mode` in the chart (`BRICKIE_GVISOR`, `--gvisor`):

- `auto` (default): use gVisor when the `gvisor` runtime class exists, otherwise warn and use the default runtime. kind and other clusters without gVisor keep working unchanged.
- `on`: fail at startup if the runtime class is missing.
- `off`: never use gVisor.

In `auto` mode, a pod that lands on a node without runsc (for example a fresh node the installer has not reached yet) is recreated once with the default runtime, and the downgrade is logged on the turn. Every turn logs which runtime it ran under. Overlayfs snapshots work unchanged under gVisor: layer assembly and checkpoints happen on the host, and the driver runs `sync` inside the sandbox before each checkpoint so buffered writes reach the workspace first.

To verify a sandbox is running under gVisor, run a command turn with `dmesg | head`. gVisor prints its own boot lines instead of the host kernel's.

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
| `--context` | `BRICKIE_KUBE_CONTEXT` | current context or in-cluster |
| `--slots` | `BRICKIE_SLOTS` | `10` |
| `--object-store` | `BRICKIE_OBJECT_STORE` | none |
| `--overlay` | `BRICKIE_OVERLAY` | `false` |
| `--gvisor` | `BRICKIE_GVISOR` | `auto` |
| `--injector`, `--injector-secret`, `--injector-image` | `BRICKIE_INJECTOR*` | disabled |
| `--warm-images`, `--warm-replicas` | `BRICKIE_WARM_IMAGES`, `BRICKIE_WARM_REPLICAS` | none, `2` |

`--credentials` is a comma-separated list of environment variable names. The Kubernetes driver copies those variables from the worker process into each sandbox pod (sealed by the injector when it is enabled). The local driver ignores it: local sandboxes receive no host environment variables by design.

## Other SDKs

Python:

```bash
pip install -e sdks/python
cd examples/python && python command.py
```

Go:

```bash
cd examples/go/command && go run .
```

The Go SDK starts the local sidecar with the environment of the current process. In embedded local mode, run `brickie worker --driver local` yourself with the matching `HATCHET_CLIENT_*` variables.

## API

TypeScript:

```ts
import { sandbox } from '@hatchet/sandbox';

const sbx = sandbox(hatchet);

const session = hatchet.durableTask({
  name: 'agent-session',
  executionTimeout: '1h',
  fn: async (input, ctx) => {
    const t1 = await sbx.executePrompt(ctx, 'create haiku.txt with a haiku');
    const t2 = await sbx.executeCommand(ctx, 'cat haiku.txt', { from: t1.snapshotId });
    const [a, b] = await sbx.executeAll(ctx, [
      { prompt: 'make it darker', from: t2.snapshotId },
      { prompt: 'make it funnier', from: t2.snapshotId },
    ]);
    return { a, b };
  },
});
```

Methods:

- `executeCommand(ctx, cmd, opts?)` runs a shell command in the workspace. Returns `{ stdout, stderr, exitCode }`. A non-zero exit code does not fail the turn.
- `executePrompt(ctx, prompt, opts?)` runs one Claude turn in the workspace. Returns `{ reply, files }`.
- `createFunction(name, fn)` registers a JavaScript function that runs inside the sandbox. Call it with `.run(ctx, input, opts?)`. The sandbox image must include node. TypeScript only.
- `executeAll(ctx, items)` runs several turns in parallel as one durable step.

Every method returns `{ output, snapshotId }`. Pass `{ from: snapshotId }` to start from a snapshot. Passing the same `from` to two calls forks that snapshot. Omit it to start from an empty workspace.

The Python SDK exposes `Sandbox` with `execute_command`, `execute_prompt`, and `execute_all`. The Go SDK exposes `sandbox.New` with `ExecuteCommand`, `ExecutePrompt`, and `ExecuteAll`.

Turn timeouts and default images are provider settings, not SDK settings. Set them on `brickie worker` or in the Helm chart values.

## Observability

Every turn streams logs and traces to Hatchet Cloud, visible on the run in the dashboard.

- The provider logs each phase of a turn: image resolution, snapshot restore with size, pod or sbx sandbox lifecycle, ready wait duration, execution time, and checkpoint. When a sandbox pod never becomes ready, the error includes the pod phase, container states, and scheduling conditions.
- The worker registers Hatchet's OpenTelemetry instrumentor, so every task run produces spans sent to Hatchet's OTLP collector. Connection settings come from the same `HATCHET_CLIENT_*` variables the worker already uses.
- The TypeScript SDK registers the instrumentor on the client side too, so triggering spans link to worker spans through traceparent propagation, and third-party backend turns log the backend session lifecycle. Every spawned turn carries `brickie.fn`, `brickie.image`, and `brickie.from` as run metadata for filtering in the dashboard.

Set `BRICKIE_OTEL=false` to disable tracing on either side. Log lines always flow.

## Snapshots

A snapshot is a gzipped tar of the workspace, stored by the provider under `--snapshot-dir` and named after the run id of the turn that produced it. Snapshots are immutable. Retried turns overwrite their own snapshot, so replays are safe. Local snapshots older than 7 days are pruned when the provider starts.

With `--object-store` (`s3://`, `gs://`, `azblob://`), snapshots are offloaded to object storage with the local directory acting as a cache, and multiple provider replicas can share them. On Kubernetes with the CSI driver enabled, snapshots are overlayfs layers checkpointed on the node instead of tars.
