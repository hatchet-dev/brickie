# brickie

brickie runs agent sandboxes as Hatchet tasks.

A sandbox is a Docker Sandboxes micro-VM. Each agent turn runs in the sandbox as a Hatchet task. Each turn makes a snapshot of the workspace. A snapshot does not change. Any snapshot can start a new sandbox. A session can continue or fork from any turn. The files and the conversation are in the snapshot.

## Requirements

- Docker Desktop on macOS. Docker Sandboxes is part of Docker Desktop.
- Node and pnpm.
- Homebrew. The setup script uses Homebrew to install the `sbx` CLI.

## Run the demo

Run this command:

```bash
hack/run-demo.sh
```

The script does the setup step by step. Before each step, it shows the command and asks for approval. The script:

1. Installs and configures Docker Sandboxes.
2. Starts Hatchet with Docker Compose.
3. Makes a Hatchet token and writes it to `.env`.
4. Builds the sandbox provider in a Go container.
5. Starts the sandbox provider.
6. Starts the demo session.

The script is safe to run again at any time. It skips the steps that are complete.

The demo is one durable task with the name `agent-session`. The task creates a sandbox. The sandbox stays alive for the full session. Each message runs as one turn in that sandbox:

- Type text to send a prompt to Claude. Claude keeps the conversation between turns.
- Type `!` and a command to run a shell command.
- Type `/restore` and a snapshot ID to start a new sandbox from that snapshot.
- Type `/exit` to end the session.

After each turn, the demo shows the snapshot ID and the time for each step. Open [http://localhost:8888](http://localhost:8888) to see the session and its turns in the Hatchet dashboard. Log in with `admin@example.com` and `Admin123!!`. The demo code is in `examples/typescript/demo.ts`.

## TypeScript SDK

A sandbox is one micro-VM. A Hatchet durable task creates the sandbox. The durable task is the agent session. Commands and Claude turns run in the sandbox. The task kills the sandbox when the work is done.

```ts
import '@hatchet/sandbox';

const session = hatchet.durableTask({
  name: 'agent-session',
  executionTimeout: '1h',
  fn: async (input, ctx) => {
    const sandbox = await hatchet.sandbox.create(ctx);
    await sandbox.prompt('create haiku.txt with a haiku');
    const haiku = await sandbox.commands.run('cat haiku.txt');

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
- `options.template` selects a Docker Sandboxes template. The default is the `claude` template. It has the Claude CLI, Node, git, and a shell.
- `options.from` starts the sandbox from a snapshot. Two sandboxes with the same `from` are a fork.

### Use the sandbox

- `sandbox.commands.run(cmd)` runs a shell command. It returns `{ stdout, stderr, exitCode }`. A non-zero exit code does not cause an error.
- `sandbox.prompt(text)` runs one Claude turn. It returns `{ reply, files }`. Claude keeps the conversation between turns.
- `sandbox.kill()` removes the sandbox. The provider also removes a sandbox after 15 minutes without a turn. Set `--sandbox-idle-ttl` on the provider to change this time.

Each call is one Hatchet task. Each result also has `snapshotId` and `timings`. The durable task that makes the calls is the session.

### Build an interactive session

Make one durable task. Create one sandbox in it. Then repeat these steps:

1. Wait for the next message with `ctx.waitForEvent`.
2. Run `sandbox.prompt` or `sandbox.commands.run`.
3. Send the result to the client with `ctx.putStream`.

The client sends messages with `hatchet.events.push`. The client reads results with `hatchet.runs.subscribeToStream`. See `examples/typescript/demo.ts`.

### Connect to Hatchet

Set `HATCHET_CLIENT_TOKEN` to use a Hatchet instance. The provider then runs the turns.

Without `HATCHET_CLIENT_TOKEN`, the example starts an embedded Hatchet engine. It also starts the provider. This needs the `brickie` binary. Put it on `PATH`, or set `BRICKIE_BIN` to its location.

## Use an external Hatchet instance

The Hatchet in Docker Compose is the default. To use Hatchet Cloud or a self-hosted Hatchet instance:

1. Get a token from the tenant.
2. Put the token in `.env` as `HATCHET_CLIENT_TOKEN`. Replace the token from the script.
3. Remove the line `HATCHET_CLIENT_TLS_STRATEGY=none`. This line is only for the local Hatchet.
4. Run `hack/run-demo.sh` again.

The script finds the external token. It does not start the local Hatchet. It restarts the provider with that instance.
