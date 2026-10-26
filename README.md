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
- `files.read` and `files.write` carry up to `--max-file-bytes` (2 MiB) through a Hatchet task. Larger files go through the provider's `--store` and never enter Hatchet. On a laptop that is a directory (`file://`); in production, S3, GCS, or Azure Blob. `files.download` and `files.upload` move files between the workspace and an https URL. The sandbox needs no network for any of this.
- In a durable task, each call is a step in the event log. Outside one, each call is a plain task run.

The full reference is in TypeDoc. Run `pnpm docs` in `sdks/typescript` and open `sdks/typescript/docs/index.html`.

## Isolation

On a laptop, a sandbox is a plain container on the local Docker engine. Good for development and CI. It is not a VM. For untrusted agents, run the provider with `--driver kubernetes`. Each sandbox is then an [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) claim bound to a pre-started pod, under gVisor or Kata Containers when the cluster has the runtime class. `hack/e2e-kind.sh` runs the whole path on a local kind cluster. `docs/SECURITY.md` describes the trust boundaries.

Each sandbox runs [`sandboxd`](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/packages/sandboxd/USER_GUIDE.md), the agent-sandbox runtime daemon, as its process. The provider injects the binary and talks to it with a bearer token: commands over gRPC, files over REST. The daemon starts commands, streams their output, and kills the whole process group on a timeout. The image needs only `sh`, and `tar` for snapshots.

No host environment variables reach a sandbox. The provider removes a sandbox after `--sandbox-idle-ttl` without a call, or after the `idleTimeoutMs` given at create. Its snapshots stay, so `connect` can bring it back.

## Run on Kubernetes

The Helm chart in `helm/brickie` installs the provider and everything it needs: agent-sandbox for sandbox pods and warm pools, Kata Containers (the default runtime, one micro-VM per sandbox), and a NetworkPolicy for sandbox pods. All subcharts are bundled.

```bash
helm upgrade --install brickie ./helm/brickie --namespace brickie --create-namespace \
  --set hatchet.token=$HATCHET_CLIENT_TOKEN \
  --set store='s3://my-bucket?prefix=brickie/'
```

What to set up first (nodes with KVM, a bucket and its IAM role, a CNI that enforces NetworkPolicy, a Hatchet tenant with gRPC operators) is in [helm/brickie/README.md](helm/brickie/README.md).

## Release

The provider image and the SDK are versioned apart, the same way as the Hatchet engine and its SDKs.

- **Provider image.** An annotated tag `v<version>` builds and pushes `ghcr.io/hatchet-dev/brickie:<version>`. The tag must match `appVersion` in `helm/brickie/Chart.yaml`, or the workflow stops before building.
- **TypeScript SDK.** Bump `version` in `sdks/typescript/package.json` and merge to `main`. The workflow publishes `@hatchet-dev/sandbox` when that version is not on npm yet, and creates the GitHub release `ts/<version>`.

```bash
git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0
```

## Use an external Hatchet instance

1. Get a token from the tenant. The tenant needs gRPC operators enabled and Hatchet v0.110 or newer.
2. Put it in `.env` as `HATCHET_CLIENT_TOKEN`. Remove the line `HATCHET_CLIENT_TLS_STRATEGY=none`.
3. Run `hack/run-demo.sh` again. It does not start the local Hatchet.
