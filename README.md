# brickie

Agent sandboxes as Hatchet durable tasks. A sandbox is a Docker Sandboxes micro-VM. Every agent turn runs inside it as a Hatchet task and ends with an immutable workspace snapshot. The session that drives the turns is a durable task. Any snapshot can start a new sandbox, so you can resume or fork a session at any turn, files and conversation included.

## Requirements

- Docker Desktop on macOS (Docker Sandboxes ships with it)
- Node and pnpm
- Homebrew (installs the `sbx` CLI on first run)

## Run it

```bash
hack/run-demo.sh
```

The script is a guided setup. It explains each step and asks before running it. It sets up Docker Sandboxes, starts Hatchet with docker compose, saves a token into `.env`, builds and starts the provider, and opens the demo session.

The demo is one long-lived durable task, `agent-session`, with one sandbox for the whole session. It waits for each message and runs the turn inside that sandbox:

- `<text>` runs a Claude turn. The conversation carries across turns.
- `!<command>` runs a shell command.
- `/restore <snapshot-id>` replaces the sandbox with a new one forked from that snapshot.
- `/exit` ends the session.

Each turn prints its snapshot ID and a latency breakdown. The dashboard at [http://localhost:8888](http://localhost:8888) (`admin@example.com` / `Admin123!!`) shows the session run and every turn under it. The demo source is `examples/typescript/demo.ts`.

## TypeScript SDK

```ts
import '@hatchet/sandbox';

const session = hatchet.durableTask({
  name: 'agent-session',
  executionTimeout: '1h',
  fn: async (input, ctx) => {
    const sbx = hatchet.sandbox();
    const t1 = await sbx.executePrompt(ctx, 'create haiku.txt with a haiku');
    const t2 = await sbx.executeCommand(ctx, 'cat haiku.txt');

    const fork = hatchet.sandbox({ from: t2.snapshotId });
    const t3 = await fork.executePrompt(ctx, 'make it darker');

    await sbx.destroy(ctx);
    await fork.destroy(ctx);
    return { t2, t3 };
  },
});
```

`hatchet.sandbox()` is one sandbox. Every call on the handle runs in that same micro-VM, so files and state carry from turn to turn. Each turn is a Hatchet task and ends with a snapshot. The sandbox is created on the first turn, not when the handle is made.

By default the sandbox uses the Docker Sandboxes template for the `claude` agent, which has the Claude CLI, Node, git, and a shell. `hatchet.sandbox({ image })` picks another sbx template.

Every method returns `{ output, snapshotId, timings }`:

- `executeCommand(ctx, cmd)` runs a shell command. Output is `{ stdout, stderr, exitCode }`. A non-zero exit does not fail the turn.
- `executePrompt(ctx, prompt)` runs one Claude turn. Output is `{ reply, files }`.
- `createFunction(name, fn)` registers a JavaScript function that runs inside the sandbox. Call it with `.run(ctx, input)`.
- `executeAll(ctx, items)` runs several turns as one durable step. On one sandbox they run in order.
- `destroy(ctx)` removes the sandbox. Sandboxes idle for 15 minutes are removed anyway (`--sandbox-idle-ttl` on the provider).

## Using an external Hatchet instance

The compose Hatchet is just a default. To use Hatchet Cloud or any self-hosted instance:

1. Put your tenant's token in `.env` as `HATCHET_CLIENT_TOKEN`, replacing the minted one.
2. Remove the `HATCHET_CLIENT_TLS_STRATEGY=none` line. It is only for the local compose engine.
3. Re-run `hack/run-demo.sh`. It detects the external token, skips the compose engine, and restarts the provider against your instance.
