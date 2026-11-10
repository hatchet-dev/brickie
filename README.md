# brickie

brickie runs agent sandboxes as Hatchet tasks.

A sandbox is a container with a shell and a workspace. Every call on it is one Hatchet task. Every call that changes the workspace makes a snapshot. A snapshot never changes, any snapshot can start a new sandbox, and a sandbox can be picked up again later by its ID.

## Try it

You need Docker, Node, and pnpm.

```bash
hack/run-demo.sh        # the SDK walkthrough
hack/run-demo.sh fs     # the file API only
```

The script starts Hatchet with Docker Compose, writes a token to `.env`, builds and starts the provider, and runs the demo. It asks before each step and is safe to run again.

The demo is one durable task, `agent-session`, in `examples/typescript/demo.ts`. It calls each SDK function once and prints the snapshot ID and timings for each step. The Hatchet dashboard is at [http://localhost:8888](http://localhost:8888), login `admin@example.com` / `Admin123!!`.

## Use the SDK

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

- `create` starts a sandbox, `connect(id)` continues one, `list({ metadata })` finds them, `delete({ metadata })` removes them with everything stored for them.
- `sandbox.exec(cmd)` runs a shell command. `sandbox.files` reads and writes files. `sandbox.kill()` stops the sandbox but keeps its snapshots.
- Files up to 2 MiB travel inside the Hatchet task. Bigger files go through the provider's store and never enter Hatchet.
- Inside a durable task every call is a step in the event log, so a crash picks up where it left off.

The SDK is [`@hatchet-dev/sandbox`](https://www.npmjs.com/package/@hatchet-dev/sandbox), developed in [hatchet-dev/hatchet](https://github.com/hatchet-dev/hatchet/tree/main/sdks/typescript-sandbox). Its README has the full API.

## Run the provider

The provider is one process, `brickie worker`. It connects out to Hatchet and needs nothing inbound. Run one per Hatchet tenant.

On a machine with Docker, download the release archive for your platform and start it with the tenant token:

```bash
tar -xzf brickie_0.1.2_Linux_x86_64.tar.gz
HATCHET_CLIENT_TOKEN=... ./brickie worker
```

Sandboxes are plain containers on the local Docker engine. That is fine for development and for agents you trust. It is not a VM.

On Kubernetes, use the `brickie` chart from [hatchet-charts](https://github.com/hatchet-dev/hatchet-charts). Each sandbox is then its own pod, under Kata Containers or gVisor when the cluster has them, and the provider talks to it over mutual TLS. The [chart README](https://github.com/hatchet-dev/hatchet-charts/blob/main/charts/brickie/README.md) lists what to set up first.

```bash
helm repo add hatchet https://hatchet-dev.github.io/hatchet-charts
helm install brickie hatchet/brickie --namespace brickie --create-namespace \
  --set hatchet.token=$HATCHET_CLIENT_TOKEN \
  --set store='s3://my-bucket?prefix=brickie/'
```

Either way, snapshots, sandbox metadata, and large files live in `--store`: a bucket in production, a local directory on a laptop. `brickie worker --help` lists every flag and its `BRICKIE_*` environment variable. Release builds run in FIPS 140-3 mode with the certified Go Cryptographic Module; `brickie version` shows it.

## How it works

Every sandbox runs [sandboxd](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/packages/sandboxd/USER_GUIDE.md), the agent-sandbox daemon, as its process. The provider talks to it with a bearer token: commands over gRPC, files over REST. The sandbox image only needs `sh` and `tar`. No host environment variables reach a sandbox, and a sandbox is removed after it sits idle for `--sandbox-idle-ttl`. Its snapshots stay until `delete`.

`docs/SECURITY.md` describes the trust boundaries in detail.

## Use your own Hatchet

1. Get a token from the tenant. It needs gRPC operators enabled and Hatchet v0.110.0 or newer.
2. Put it in `.env` as `HATCHET_CLIENT_TOKEN` and remove the line `HATCHET_CLIENT_TLS_STRATEGY=none`.
3. Run `hack/run-demo.sh` again. It skips the local Hatchet.
