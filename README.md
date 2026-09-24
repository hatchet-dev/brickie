# brickie

Agent sandboxes as durable tasks.

brickie gives every agent turn a fresh, isolated sandbox and a durable record. Each turn runs as one Hatchet durable task inside its own Docker Sandboxes micro-VM; when it finishes, the workspace is saved as an immutable snapshot. Later turns resume from a snapshot, or fork it — two turns started from the same snapshot are two independent futures of the same workspace. Because turns are durable tasks, retries, timeouts, replay, and observability come from the orchestrator, not from the sandbox.

What that buys you:

- **Isolation**: agent code runs in a Docker Sandboxes micro-VM per turn, never on your machine. No host environment variables are injected; the per-turn workspace is the only shared path.
- **Durability**: an agent session survives crashes and restarts. The event log replays the decisions; snapshots replay the filesystem.
- **Time travel**: every turn's snapshot id is in the task output. Resume any point of any session, or fork it into parallel branches — the conversation forks with the files, because the transcript rides the snapshot chain.
- **One mental model**: a session is just a durable task; a turn is just a child task. Everything composes with the rest of your Hatchet workflows.

## Quickstart

Requirements: Docker Desktop (macOS), Node, pnpm. No Go toolchain — the provider is built inside a golang container.

```bash
hack/run-demo.sh
```

The script sets up Docker Sandboxes (installs the `sbx` CLI, signs in, authenticates Claude inside sandboxes — browser steps happen once), brings up Hatchet with `docker compose up`, mints a Hatchet token into `.env` on first run, builds and starts the sandbox provider, and drops you into the demo session. Re-running it is always safe.

The demo is an interactive session: one long-lived durable `agent-session` task holds the conversation, and every message you type runs as a child turn in a fresh sandbox. Plain text goes to Claude, `!command` runs a shell command, `/restore <id>` forks from any earlier snapshot, `/exit` ends the session. Every turn prints its snapshot id and a latency breakdown.

The Hatchet dashboard at [http://localhost:8888](http://localhost:8888) (login `admin@example.com` / `Admin123!!`) shows the session run, each turn, and their logs.

## Writing your own programs

Export the variables from `.env`, then any Hatchet TypeScript program can run sandbox turns:

```ts
import '@hatchet/sandbox';

const session = hatchet.durableTask({
  name: 'agent-session',
  executionTimeout: '1h',
  fn: async (input, ctx) => {
    const sbx = hatchet.sandbox();
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

Methods, each returning `{ output, snapshotId, timings }`:

- `executeCommand(ctx, cmd, opts?)` runs a shell command in the workspace. Output is `{ stdout, stderr, exitCode }`; a non-zero exit does not fail the turn.
- `executePrompt(ctx, prompt, opts?)` runs one Claude turn. Output is `{ reply, files }`.
- `createFunction(name, fn)` registers a JavaScript function that runs inside the sandbox; call it with `.run(ctx, input, opts?)`.
- `executeAll(ctx, items)` runs several turns in parallel as one durable step.

Pass `{ from: snapshotId }` to start from a snapshot; pass the same `from` to two calls to fork it; omit it for an empty workspace. `image` selects an sbx template. For long-running sessions, loop on `ctx.waitForEvent` and spawn turns per message — `examples/typescript/demo.ts` is the reference.

Your program is a plain Hatchet worker: it declares the session task and triggers it. The sandbox provider started by `hack/run-demo.sh` executes the turns.

## How it works

- **Provider** (`bin/brickie worker`, managed by `hack/run-demo.sh`): registers with Hatchet as a gRPC operator and executes `brickie-sandbox` durable tasks. It runs on the host because Docker Sandboxes micro-VMs are managed by a host daemon — one micro-VM per turn: create, restore the snapshot, exec, snapshot, remove.
- **Engine** (`internal/engine`, Go): the turn contract and durable task. All execution logic lives here; the SDK only triggers tasks by name.
- **Snapshots**: gzipped tars of the workspace under `~/.brickie/snapshots`, keyed by run id so retried turns overwrite their own snapshot and replays stay safe. Pruned after 7 days.
- **Credentials**: no host environment variables reach a sandbox. Claude authenticates through the credential Docker Sandboxes stores in its agent kit.
- **Sessions**: a durable task that loops on `waitForEvent`, spawns each turn as a child, and streams results back — the session's state is fully reconstructible from the event log.

See `docs/ARCHITECTURE.md` for the full design.

## Observability

Every turn streams logs to Hatchet, and both the provider and the SDK register Hatchet's OpenTelemetry instrumentor, so triggering spans link to execution spans and every turn carries `brickie.fn`, `brickie.image`, and `brickie.from` metadata. Set `BRICKIE_OTEL=false` to disable tracing.
