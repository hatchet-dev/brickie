# brickie architecture

brickie runs agent sandboxes as Hatchet durable tasks. Every sandbox turn is one task; every turn ends with a workspace snapshot that later turns can resume or fork. This document describes the design and where each part lives in the repository.

## The bet

Sandboxes are a complement to orchestration. Companies that need sandboxes also need durable orchestration, retries, and observability, which is what Hatchet sells. brickie is therefore not trying to win on isolation primitives or filesystem technology: sandboxes are Docker Sandboxes micro-VMs, state and reliability come from Hatchet, and brickie is the thin engine that ties them together.

## State model

Agent state has two layers:

| Layer | Owner | Mechanism |
| --- | --- | --- |
| Agent loop state (messages, tool calls, results) | Hatchet durable execution | Event log and replay |
| Filesystem state | brickie snapshots | gzipped tar of the workspace per turn |

The durable log answers "what has the agent decided and observed". The filesystem snapshot answers "what does the world look like now". Every turn returns its snapshot id as part of the task output, so the durable log indexes the filesystem history.

## Turn lifecycle

A turn is one durable Hatchet task named `brickie-sandbox`. The input carries the function to run (`$command`, `$prompt`, or a user function), the input payload, and an optional snapshot id. The provider:

1. Starts a fresh Docker Sandboxes micro-VM for the turn.
2. Restores the workspace from the snapshot (empty when no snapshot id is given).
3. Runs the turn.
4. Saves the workspace as a new snapshot named after the run id and removes the sandbox.
5. Returns `{ output, snapshotId, timings }`.

Retries are safe because the snapshot id in the input never changes and the output snapshot is keyed by run id. A retried turn starts from the same filesystem state and overwrites its own snapshot. A fork is two turns started from the same snapshot id. This makes arbitrary shell commands idempotent from the orchestrator's point of view.

## Conversation continuity

Prompt turns carry the conversation, not just the files: the engine keeps a rolling transcript in `.conversation.json` inside the workspace, feeds it to Claude on each turn, and appends the new exchange before the snapshot is taken. Because the transcript rides the snapshot chain, resuming a snapshot resumes the conversation and forking a snapshot forks it. The transcript is data authored by the user and the model — no credentials or harness internals enter snapshots — and it is capped so prompts stay bounded.

## Sessions

A session is one long-lived durable task that loops on `waitForEvent` for user messages, spawns each turn as a child task, carries the snapshot lineage in its own replayable state, and streams turn results back to the caller. `examples/typescript/demo.ts` is the reference implementation.

## Components

### TypeScript SDK (`sdks/typescript`)

A thin client. It spawns `brickie-sandbox` durable child tasks by name from the caller's durable context and returns typed results. It makes no placement decisions: with `HATCHET_CLIENT_TOKEN` set, the provider registered in that tenant runs the turns; without one, it starts an embedded Hatchet engine and spawns `brickie worker` as a sidecar (a development convenience — the supported path is the compose stack).

`createFunction` serializes a JavaScript function into the turn input (`fnSource`); the engine runs it inside the sandbox with a small node runner.

### Engine (`internal/engine`)

Defines the turn contract, the durable task, the operator session, and the snapshot store: a directory of gzipped tars keyed by run id, pruned after 7 days.

### Local driver (`internal/driver/local`)

One Docker Sandboxes micro-VM per turn through the `sbx` CLI (Docker publishes no Go client): create with the host workspace bind-mounted, exec the turn, tar the workspace, remove the sandbox. No host environment variables are injected; Claude authenticates through the credential Docker Sandboxes stores in the sandbox agent kit.

### Provider (`brickie worker`)

The provider registers with Hatchet as a gRPC operator (falling back to a classic worker when the engine predates operator support) and executes `brickie-sandbox` turns with the local driver. Desired state lives in Hatchet's run history, so retries, timeouts, and replay come from the orchestrator.

## Deployment

`docker compose up` runs Hatchet (lite + Postgres). The provider runs on the host because Docker Sandboxes micro-VMs are managed by a host daemon (`sandboxd`) that is only reachable over a host Unix socket — a container cannot drive it. To keep the host toolchain-free, `hack/run-demo.sh` builds the provider inside a golang container and manages it as a background process; it also does the one-time Docker Sandboxes setup, mints a Hatchet token into `.env`, and starts the demo session. User programs run on the host with node and the TypeScript SDK, pointed at the stack by `HATCHET_CLIENT_TOKEN`.

Docker publishes Linux builds of Docker Sandboxes whose runtime uses KVM, so on Linux hosts (and macOS hosts with nested virtualization) a fully containerized `sandboxd` service is a plausible future step.
