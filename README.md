# brickie

Agent sandboxes as durable tasks. Every agent turn runs as a Hatchet durable task inside its own Docker Sandboxes micro-VM. Each turn ends with an immutable workspace snapshot. Later turns can resume or fork any snapshot, files and conversation included. See `docs/ARCHITECTURE.md` for the design.

## Requirements

- Docker Desktop on macOS (Docker Sandboxes ships with it)
- Node and pnpm
- Homebrew (installs the `sbx` CLI on first run)

## Run it

```bash
hack/run-demo.sh
```

The script is a guided setup. It explains each step and asks before running it. It sets up Docker Sandboxes, starts Hatchet with docker compose, saves a token into `.env`, builds and starts the provider, and opens the demo session.

In the demo, every message runs as one durable turn in a fresh sandbox:

- `<text>` runs a Claude turn. The conversation carries across turns.
- `!<command>` runs a shell command.
- `/restore <id>` forks a new sandbox from any earlier snapshot.
- `/exit` ends the session.

Each turn prints its snapshot ID and a latency breakdown. The dashboard at [http://localhost:8888](http://localhost:8888) (`admin@example.com` / `Admin123!!`) shows every run. The demo source is `examples/typescript/demo.ts`.

## TypeScript SDK

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

Every method returns `{ output, snapshotId, timings }`:

- `executeCommand(ctx, cmd, opts?)` runs a shell command. Output is `{ stdout, stderr, exitCode }`. A non-zero exit does not fail the turn.
- `executePrompt(ctx, prompt, opts?)` runs one Claude turn. Output is `{ reply, files }`.
- `createFunction(name, fn)` registers a JavaScript function that runs inside the sandbox. Call it with `.run(ctx, input, opts?)`.
- `executeAll(ctx, items)` runs several turns in parallel as one durable step.

Pass `{ from: snapshotId }` to continue from a snapshot. Passing the same `from` twice forks it. Omit it for an empty workspace. For long-running sessions, loop on `ctx.waitForEvent` and spawn one turn per message. `examples/typescript/demo.ts` is the reference.

When `HATCHET_CLIENT_TOKEN` is set, the SDK talks to that Hatchet instance and the provider executes the turns. When it is unset, the SDK starts an embedded Hatchet engine and the provider itself, so a bare program still runs with nothing else installed.

## Using an external Hatchet instance

The compose Hatchet is just a default. To use Hatchet Cloud or any self-hosted instance:

1. Put your tenant's token in `.env` as `HATCHET_CLIENT_TOKEN`, replacing the minted one.
2. Remove the `HATCHET_CLIENT_TLS_STRATEGY=none` line. It is only for the local compose engine.
3. Re-run `hack/run-demo.sh`. It detects the external token, skips the compose engine, and restarts the provider against your instance.
