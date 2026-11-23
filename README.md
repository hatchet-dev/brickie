# Brickie

Run sandboxes with filesystem snapshot/restore in your own Kubernetes cluster or cloud account.

[![release](https://img.shields.io/github/v/release/hatchet-dev/brickie?display_name=tag&sort=semver)](https://github.com/hatchet-dev/brickie/releases)
[![license](https://img.shields.io/github/license/hatchet-dev/brickie)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/hatchet-dev/brickie)](go.mod)
[![e2e-docker](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-docker.yaml/badge.svg?branch=main)](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-docker.yaml)
[![e2e-kind](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-kind.yaml/badge.svg?branch=main)](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-kind.yaml)
[![FIPS 140-3](https://img.shields.io/badge/FIPS%20140--3-Go%20Cryptographic%20Module-2b6cb0)](docs/security.md)
[![signed images](https://img.shields.io/badge/images-cosign%20signed%20%2B%20SBOM-2b6cb0)](docs/security.md#verifying-images)

Sandboxes are built on top of [agent-sandbox](https://agent-sandbox.sigs.k8s.io/) with custom snapshot/restore implemented on top of object storage. They are designed to be called from [`@hatchet-dev/sandbox`](https://www.npmjs.com/package/@hatchet-dev/sandbox) and support the ability to take a [coordinated snapshot](https://hatchet.run/blog/agent-statefulness) of agent state. This means that every sandbox call is a retryable and observable [Hatchet](https://hatchet.run?utm_source=brickie-github&utm_medium=referral&utm_campaign=brickie-launch&utm_content=readme) task.

Each sandbox session is a container with a shell and a workspace and runs in your Kubernetes cluster with flexible runtime support, including [Kata](https://katacontainers.io/), [gVisor](https://gvisor.dev/) and [AWS Nitro](https://docs.aws.amazon.com/eks/latest/userguide/isolated-sandbox.html). Every call that changes the workspace produces a snapshot, which can be used to fork a new sandbox or resume an existing session.

> Brickie is in beta. Flags, chart values and the wire contract may change before 1.0. See the [changelog](CHANGELOG.md).

**Docs: [here](./docs/)**

## Features

## Try it

The [quickstart](docs/quickstart.md) runs a local Hatchet with one Docker Compose file, the provider as a binary, and a first sandbox session from a single TypeScript file. No clone needed. From a checkout, `hack/run-demo.sh` does the same in one go.

## Use it from a task

```bash
pnpm add @hatchet-dev/sandbox @hatchet-dev/typescript-sdk
```

```ts
import "@hatchet-dev/sandbox";

const session = hatchet.durableTask({
  name: "agent-session",
  fn: async (input) => {
    const sandbox = await hatchet.sandbox.create({
      id: input.conversationId,
      template: "alpine:3",
    });
    await sandbox.files.write("haiku.txt", "an old silent pond");
    const haiku = await sandbox.exec("cat haiku.txt");
    await sandbox.kill();
    return { haiku: haiku.stdout };
  },
});

// later, from anywhere with a Hatchet client
const again = await hatchet.sandbox.connect("conv-42");
```

The SDK is [`@hatchet-dev/sandbox`](https://www.npmjs.com/package/@hatchet-dev/sandbox), developed in [hatchet-dev/hatchet](https://github.com/hatchet-dev/hatchet/tree/main/sdks/typescript-sandbox).

## Run it

On a machine with Docker, grab a [release](https://github.com/hatchet-dev/brickie/releases) and start it with a tenant token. Sandboxes are plain containers, for local testing only.

```bash
HATCHET_CLIENT_TOKEN=... ./brickie worker
```

On Kubernetes, use the [Helm chart](https://github.com/hatchet-dev/hatchet-charts/tree/main/charts/brickie). Every sandbox is its own pod under Kata Containers, gVisor or EKS Nitro, and the provider talks to it over mutual TLS.

```bash
helm repo add hatchet https://hatchet-dev.github.io/hatchet-charts
helm install brickie hatchet/brickie -n brickie --create-namespace \
  --set hatchet.token=$HATCHET_CLIENT_TOKEN \
  --set store='s3://my-bucket?prefix=brickie/'
```

The [install docs](docs/install/README.md) cover both in detail, and [configuration](docs/install/configuration.md) lists every flag.

## Brickie versus agent-sandbox

Under the hood, `brickie` uses the [agent-sandbox](https://agent-sandbox.sigs.k8s.io/) controller and warm pools to run sandbox pods and adds the durability, storage and observability layer on top:

1. Every `brickie` sandbox turn produces an immutable snapshot in your object store, keyed by sandbox and run ID. These snapshots can be forked into new sandboxes, or used to resume an existing session. On `agent-sandbox`, state is persisted through a PVC per sandbox, which forces a cold start, while the only point-in-time snapshot is GKE-specific.

2. Every call is a Hatchet task, so it is retried, observable and replayed from its own snapshot. A retried call finds the snapshot it already stored and does not run the command twice.

3. The same code path runs on a local Docker engine, so the Kubernetes cluster is not needed for development.

## Learn more

- [Architecture](docs/architecture.md), how turns, snapshots and the store fit together
- [Security](docs/security.md), trust boundaries, isolation and image verification
- [Drivers](docs/drivers.md) and [extending Brickie](docs/extending.md) with your own
- [Wire contract](docs/wire-contract.md), what the SDK sends and gets back
- [Hatchet docs](https://docs.hatchet.run?utm_source=brickie-github&utm_medium=referral&utm_campaign=brickie-launch&utm_content=readme), the task engine underneath
- [Contributing](docs/contributing.md) and [AGENTS.md](AGENTS.md), the working rules

## Contributing

Pull requests are welcome from [vouched](CONTRIBUTING.md#building-trust-vouching) contributors, and the first ones should be small. [CONTRIBUTING.md](CONTRIBUTING.md) explains how to get vouched, what to work on first and how AI use is disclosed.

## License

[MIT](LICENSE)
