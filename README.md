# brickie

Universal sandbox API and session manager. Deploys into your own cloud account.

brickie runs agent sandboxes as Hatchet durable tasks. Each sandbox turn is one task. When a turn finishes, the workspace is saved as a snapshot. Later turns can resume from a snapshot or fork it.

The SDKs only trigger turns. The `brickie` engine (Go) executes them, either locally in Docker Sandboxes or in Kubernetes using [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox).

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/brickie/` | CLI. `worker` runs the sandbox provider as a Hatchet worker. `bootstrap-local` and `bootstrap-kind` set up the two supported environments. |
| `internal/engine/` | Durable task definition, snapshot store, turn contract, node runner. |
| `internal/driver/local/` | Docker Sandboxes driver. Uses the `sbx` CLI. Docker does not publish a Go client. |
| `internal/driver/kubernetes/` | Kubernetes driver. Creates agent-sandbox `Sandbox` resources with the official v1beta1 types and runs commands with pod exec. |
| `sdks/typescript`, `sdks/python`, `sdks/go` | Client SDKs. |
| `examples/typescript`, `examples/python`, `examples/go` | Runnable sessions. |
| `helm/brickie/` | Helm chart for the provider. Includes the agent-sandbox chart as a subchart. |

## Requirements

- Go 1.26 or newer to build the engine
- Docker Desktop
- For local sandboxes: Homebrew (installs the `sbx` CLI)
- For Kubernetes: `kind`, `kubectl`, `helm`
- Node and pnpm for the TypeScript SDK and examples

## Install the engine

```bash
go install github.com/hatchet-dev/brickie/cmd/brickie@latest
```

Or build from this repository:

```bash
go build -o /usr/local/bin/brickie ./cmd/brickie
```

## Local mode

Local mode uses an embedded Hatchet engine and Docker Sandboxes. No Hatchet Cloud account is needed.

```bash
brickie bootstrap-local
```

This installs the `sbx` CLI, signs in to Docker, initializes the sandbox network policy, and logs Claude in inside sandboxes. It opens a browser for the sign-in steps.

Run the TypeScript examples:

```bash
cd examples/typescript
pnpm install
pnpm command    # shell commands only, no LLM
pnpm prompt     # Claude turns with snapshot chaining and forks
pnpm harness    # Claude Agent SDK harness as a user-defined sandbox function
```

When `HATCHET_CLIENT_TOKEN` is not set, the SDK starts an embedded Hatchet engine and spawns `brickie worker --driver local` as a sidecar. If the binary is not on `PATH`, set `BRICKIE_BIN` to its location.

A successful run prints `session result: {...}` followed by `e2e ok`.

## Kubernetes mode

Kubernetes mode uses Hatchet Cloud for orchestration and a kind cluster for sandboxes.

```bash
export HATCHET_CLIENT_TOKEN=<token from your tenant>
export ANTHROPIC_API_KEY=<key>   # or CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`
brickie bootstrap-kind -n brickie
cd examples/typescript && pnpm k8s
```

`bootstrap-kind` creates the kind cluster, builds the provider image from the root `Dockerfile`, loads it into the cluster, and installs the Helm chart. If agent-sandbox is already installed on the cluster, the subchart is skipped.

When `HATCHET_CLIENT_TOKEN` is set, the SDK does not start anything. It triggers `brickie-sandbox` tasks and the provider registered in the tenant runs them. The provider creates one `Sandbox` pod per turn, restores the snapshot, runs the turn, saves the new snapshot, and deletes the pod. The default sandbox image is `alpine:3`. Use `--sandbox-image` to change it.

If you develop hatchet locally with mkcert, run `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` before connecting to Hatchet Cloud. Otherwise the gRPC connection fails with a TLS error.

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
| `--slots` | | `10` |

`--credentials` is a comma-separated list of environment variable names. The Kubernetes driver copies those variables from the worker process into each sandbox pod. Nothing else from the worker environment is passed through.

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

## Snapshots

A snapshot is a gzipped tar of the workspace, stored by the provider under `--snapshot-dir` and named after the run id of the turn that produced it. Snapshots are immutable. Retried turns overwrite their own snapshot, so replays are safe.

The provider stores snapshots on its own filesystem. Run one provider replica until an object storage backend is added.
