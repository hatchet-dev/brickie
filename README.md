# brickie

brickie runs agent sandboxes as Hatchet tasks.

A sandbox is a Docker container, or a Docker Sandboxes micro-VM. Each call on a sandbox is one Hatchet task. Each call that changes the workspace makes a snapshot. A snapshot does not change. Any snapshot can start a new sandbox. A sandbox can be continued later by its ID.

## Requirements

- Docker.
- Node and pnpm.
- For the `sbx` driver: Docker Desktop on macOS and Homebrew.

## Run the demo

```bash
hack/run-demo.sh        # the SDK walkthrough
hack/run-demo.sh fs     # the file API only
```

The script does the setup step by step and asks before each step. It starts Hatchet with Docker Compose, writes a token to `.env`, builds and starts the provider, and runs the demo. It is safe to run again.

The demo is one durable task, `agent-session`, in `examples/typescript/demo.ts`. It calls each SDK function once and prints the snapshot ID and timings for each step. Open [http://localhost:8888](http://localhost:8888) to see the task in the Hatchet dashboard. Log in with `admin@example.com` and `Admin123!!`.

## TypeScript SDK

```ts
import '@hatchet/sandbox';

const session = hatchet.durableTask({
  name: 'agent-session',
  executionTimeout: '1h',
  fn: async (input) => {
    const sandbox = await hatchet.sandbox.create({ id: input.conversationId });
    await sandbox.files.write('haiku.txt', 'an old silent pond');
    const haiku = await sandbox.exec('cat haiku.txt');
    await sandbox.kill();
    return { haiku: haiku.stdout };
  },
});

// later, from any code with a Hatchet client
const again = await hatchet.sandbox.connect('conv-42');
await again.exec('cat haiku.txt');
```

- `hatchet.sandbox.create(options)` starts a sandbox. `connect(id)` continues one. `list({ metadata })` finds them.
- `sandbox.exec(cmd)` runs a shell command. `sandbox.files` reads and writes files. `sandbox.kill()` removes the sandbox.
- In a durable task, each call is a step in the event log. Outside one, each call is a plain task run.

The full reference is in TypeDoc. Run `pnpm docs` in `sdks/typescript` and open `sdks/typescript/docs/index.html`.

## Drivers

Set `--driver` or `BRICKIE_DRIVER` on the provider.

- `docker` (default): a plain container on any Docker engine. Good for development and CI. It is not a VM.
- `sbx`: a Docker Sandboxes micro-VM. macOS only. Use it for untrusted agents. Start with `hack/run-demo.sh --driver sbx`.

No host environment variables reach a sandbox on either driver. The provider removes a sandbox after `--sandbox-idle-ttl` without a call. The sandbox's snapshots stay, so `connect` can bring it back.

## Use an external Hatchet instance

1. Get a token from the tenant.
2. Put it in `.env` as `HATCHET_CLIENT_TOKEN`. Remove the line `HATCHET_CLIENT_TLS_STRATEGY=none`.
3. Run `hack/run-demo.sh` again. It does not start the local Hatchet.
