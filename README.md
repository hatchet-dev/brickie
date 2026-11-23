# Brickie

Sandboxes as [Hatchet](https://hatchet.run?utm_source=brickie-github&utm_medium=referral&utm_campaign=brickie-launch&utm_content=readme) tasks, in your own cloud account.

[![release](https://img.shields.io/github/v/release/hatchet-dev/brickie?display_name=tag&sort=semver)](https://github.com/hatchet-dev/brickie/releases)
[![license](https://img.shields.io/github/license/hatchet-dev/brickie)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/hatchet-dev/brickie)](go.mod)
[![e2e-docker](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-docker.yaml/badge.svg?branch=main)](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-docker.yaml)
[![e2e-kind](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-kind.yaml/badge.svg?branch=main)](https://github.com/hatchet-dev/brickie/actions/workflows/e2e-kind.yaml)
[![FIPS 140-3](https://img.shields.io/badge/FIPS%20140--3-Go%20Cryptographic%20Module-2b6cb0)](https://hatchet-dev.github.io/brickie/security#files-and-snapshots)
[![signed images](https://img.shields.io/badge/images-cosign%20signed%20%2B%20SBOM-2b6cb0)](https://hatchet-dev.github.io/brickie/security#verifying-images)

A sandbox is a container with a shell and a workspace. Every call on it is one Hatchet task, so it is durable, retried and visible in the dashboard. Every call that changes the workspace makes a snapshot. A snapshot never changes, any snapshot can start a new sandbox, and a sandbox can be picked up again later by its ID.

The provider runs on your laptop with Docker or in your Kubernetes cluster, connects out to Hatchet and needs nothing inbound. Your code only talks to Hatchet.

> Brickie is in beta. Flags, chart values and the wire contract may change before 1.0. See the [changelog](CHANGELOG.md).

**Docs: [hatchet-dev.github.io/brickie](https://hatchet-dev.github.io/brickie)**

## Try it

The [quickstart](https://hatchet-dev.github.io/brickie/quickstart) runs a local Hatchet with one Docker Compose file, the provider as a binary, and a first sandbox session from a single TypeScript file. No clone needed. From a checkout, `hack/run-demo.sh` does the same in one go.

## Use it from a task

```bash
pnpm add @hatchet-dev/sandbox @hatchet-dev/typescript-sdk
```

```ts
import '@hatchet-dev/sandbox';

const session = hatchet.durableTask({
  name: 'agent-session',
  fn: async (input) => {
    const sandbox = await hatchet.sandbox.create({ id: input.conversationId, template: 'alpine:3' });
    await sandbox.files.write('haiku.txt', 'an old silent pond');
    const haiku = await sandbox.exec('cat haiku.txt');
    await sandbox.kill();
    return { haiku: haiku.stdout };
  },
});

// later, from anywhere with a Hatchet client
const again = await hatchet.sandbox.connect('conv-42');
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

The [install docs](https://hatchet-dev.github.io/brickie/install) cover both in detail, and [configuration](https://hatchet-dev.github.io/brickie/install/configuration) lists every flag.

## Learn more

- [Architecture](https://hatchet-dev.github.io/brickie/architecture), how turns, snapshots and the store fit together
- [Security](https://hatchet-dev.github.io/brickie/security), trust boundaries, isolation and image verification
- [Drivers](https://hatchet-dev.github.io/brickie/drivers) and [extending Brickie](https://hatchet-dev.github.io/brickie/extending) with your own
- [Hatchet docs](https://docs.hatchet.run?utm_source=brickie-github&utm_medium=referral&utm_campaign=brickie-launch&utm_content=readme), the task engine underneath
- [Contributing](https://hatchet-dev.github.io/brickie/contributing) and [AGENTS.md](AGENTS.md), the working rules

## Contributing

Pull requests are welcome from [vouched](CONTRIBUTING.md#building-trust-vouching) contributors, and the first ones should be small. [CONTRIBUTING.md](CONTRIBUTING.md) explains how to get vouched, what to work on first and how AI use is disclosed.

## License

[MIT](LICENSE)
