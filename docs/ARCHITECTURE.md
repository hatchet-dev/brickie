# brickie architecture

brickie runs agent sandboxes as Hatchet durable tasks. Every sandbox turn is one task; every turn ends with a workspace snapshot that later turns can resume or fork. This document describes the design and where each part lives in the repository.

## The bet

Sandboxes are a complement to orchestration. Companies that need sandboxes also need durable orchestration, retries, and observability, which is what Hatchet sells. brickie is therefore not trying to win on isolation primitives or filesystem technology: sandboxes come from Docker Sandboxes, state and reliability come from Hatchet, and brickie is the thin engine that ties them together.

## State model

Agent state has two layers today:

| Layer | Owner | Mechanism |
| --- | --- | --- |
| Agent loop state (messages, tool calls, results) | Hatchet durable execution | Event log and replay |
| Filesystem state | brickie snapshots | gzipped tar of the workspace per turn |

The durable log answers "what has the agent decided and observed". The filesystem snapshot answers "what does the world look like now". Every turn returns its snapshot id as part of the task output, so the durable log indexes the filesystem history.

## Turn lifecycle

A turn is one durable Hatchet task named `brickie-sandbox`. The input carries the function to run (`$command`, `$prompt`, or a user function), the input payload, and an optional snapshot id. The provider:

1. Restores the workspace from the snapshot (empty workspace when no snapshot id is given).
2. Runs the turn in a fresh Docker Sandboxes micro-VM.
3. Saves the workspace as a new snapshot named after the run id.
4. Returns `{ output, snapshotId, timings }`.

Retries are safe because the snapshot id in the input never changes and the output snapshot is keyed by run id. A retried turn starts from the same filesystem state and overwrites its own snapshot. A fork is two turns started from the same snapshot id. This makes arbitrary shell commands idempotent from the orchestrator's point of view.

## Sessions

A session is one long-lived durable task that loops on `waitForEvent` for user messages, spawns each turn as a child task, carries the snapshot lineage in its own replayable state, and streams turn results back to the caller. `examples/typescript/demo.ts` is the reference implementation.

## Components

### TypeScript SDK (`sdks/typescript`)

A thin client. It spawns `brickie-sandbox` durable child tasks by name from the caller's durable context and returns typed results. It makes no placement decisions:

- `HATCHET_CLIENT_TOKEN` set: the provider registered in that tenant runs the turns.
- No token: the SDK starts an embedded Hatchet engine and spawns `brickie worker` as a sidecar.

`createFunction` serializes a JavaScript function into the turn input (`fnSource`); the engine runs it inside the sandbox with a small node runner.

### Engine (`internal/engine`)

Defines the turn contract, the durable task, the operator session, and the snapshot store: a directory of gzipped tars keyed by run id, pruned after 7 days.

### Local driver (`internal/driver/local`)

Docker Sandboxes through the `sbx` CLI (Docker publishes no Go client). One micro-VM per turn: create with the host workspace bind-mounted, exec the turn, tar the workspace, remove the sandbox.

### Provider (`brickie worker`)

The provider registers with Hatchet as a gRPC operator (falling back to a classic worker when the engine predates operator support) and executes `brickie-sandbox` turns with the local driver. Desired state lives in Hatchet's run history, so retries, timeouts, and replay come from the orchestrator. The machine only ever dials out to Hatchet; there is no inbound API.

## Credentials

No environment variables are copied from the host into local sandboxes. Claude authenticates through the credential Docker Sandboxes stores in the sandbox agent kit (`brickie dev` sets it up).

## Deployment

`docker compose up -d` runs Hatchet (lite + Postgres) on the machine. `brickie dev` configures Docker Sandboxes. The TypeScript SDK plus a Hatchet token is everything a program needs.
