# brickie

brickie runs agent sandboxes as Hatchet tasks.

A sandbox is a Docker container. Each call on a sandbox is one Hatchet task. Each call that changes the workspace makes a snapshot. A snapshot does not change. Any snapshot can start a new sandbox. A sandbox can be continued later by its ID.

## Requirements

- Docker.
- Node and pnpm.

## Run the demo

```bash
hack/run-demo.sh        # the SDK walkthrough
hack/run-demo.sh fs     # the file API only
```

The script does the setup step by step and asks before each step. It starts Hatchet with Docker Compose, writes a token to `.env`, builds and starts the provider, and runs the demo. It is safe to run again.

The demo is one durable task, `agent-session`, in `examples/typescript/demo.ts`. It calls each SDK function once and prints the snapshot ID and timings for each step. Open [http://localhost:8888](http://localhost:8888) to see the task in the Hatchet dashboard. Log in with `admin@example.com` and `Admin123!!`.

## TypeScript SDK

```bash
pnpm add @hatchet-dev/sandbox @hatchet-dev/typescript-sdk
```

```ts
import '@hatchet-dev/sandbox';

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
- `sandbox.exec(cmd, { timeoutMs })` runs a shell command. A timeout returns exit code 124. `sandbox.files` reads and writes files. `sandbox.kill()` removes the sandbox.
- `files.read` and `files.write` carry up to `--max-file-bytes` (2 MiB) through a Hatchet task. Larger files go through the provider's `--object-store` and never enter Hatchet. On a laptop that is a directory (`file://`); in production, S3, GCS, or Azure Blob. `files.download` and `files.upload` move files between the workspace and an https URL. The sandbox needs no network for any of this.
- In a durable task, each call is a step in the event log. Outside one, each call is a plain task run.

The full reference is in TypeDoc. Run `pnpm docs` in `sdks/typescript` and open `sdks/typescript/docs/index.html`.

## Isolation

On a laptop, a sandbox is a plain container on the local Docker engine. Good for development and CI. It is not a VM. For untrusted agents, run brickie on Kubernetes with gVisor or Kata Containers.

Each container runs [`sandboxd`](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/packages/sandboxd/USER_GUIDE.md), the agent-sandbox runtime daemon, as its process. The provider injects the binary and talks to it on a loopback port. The daemon starts commands, streams their output, and kills the whole process group on a timeout. The image needs only `sh`, and `tar` for restore.

No host environment variables reach a sandbox. The provider removes a sandbox after `--sandbox-idle-ttl` without a call, or after the `idleTimeoutMs` given at create. Its snapshots stay, so `connect` can bring it back.

## Run the provider

The provider is one process, `brickie worker`, on a machine with Docker. It connects out to Hatchet and needs nothing inbound. Run one per Hatchet tenant. Download the archive for your platform from the GitHub release, unpack it, and start it with the tenant token:

```bash
tar -xzf brickie_0.1.0_Linux_x86_64.tar.gz
HATCHET_CLIENT_TOKEN=... ./brickie worker
```

Release binaries are built with the CMVP-certified Go Cryptographic Module and run in FIPS 140-3 mode; `brickie version` shows it. Snapshots live under `~/.brickie/snapshots` by default (`--snapshot-dir`). Files over 2 MiB need `--object-store`, a bucket or, on one machine, a `file://` directory the SDK can also reach. Flags and their `BRICKIE_*` environment variables are listed by `brickie worker --help`. This mode runs sandboxes as plain containers on the local engine, for development and single-tenant use; untrusted agents belong on the Kubernetes driver, which ships as a container image separately.

## Use an external Hatchet instance

1. Get a token from the tenant. The tenant needs gRPC operators enabled and Hatchet v0.110.0 or newer.
2. Put it in `.env` as `HATCHET_CLIENT_TOKEN`. Remove the line `HATCHET_CLIENT_TLS_STRATEGY=none`.
3. Run `hack/run-demo.sh` again. It does not start the local Hatchet.
