# Brickie

Sandboxes orchestrated as Hatchet tasks, in your own Kubernetes cluster.

> [!WARNING]
> **Beta.** Brickie is in beta. Flags, Helm chart values and the wire contract between the SDK and the provider may change before 1.0. Changes are listed in the [changelog](../CHANGELOG.md).

Brickie runs sandboxes as [Hatchet](https://hatchet.run?utm_source=brickie-docs&utm_medium=referral&utm_campaign=brickie-launch&utm_content=index) tasks. A sandbox is a container with a shell and a workspace. It is our custom sandbox provider that complements existing 3rd party providers that we support with [Hatchet Sandboxes](https://docs.hatchet.run/v1/sandboxes?utm_source=brickie-docs&utm_medium=referral&utm_campaign=brickie-launch&utm_content=index).

The provider runs locally on your laptop with Docker for local testing or in your own Kubernetes cluster for production workloads. It makes an egress connection to a running Hatchet instance using the same mechanism a Hatchet worker would with a tenant API token.

The Brickie provider and its components alays run in FIPS 140-3 mode.

The basic sandbox lifecycle events are `create`, `exec`, `kill`, and `connect`. Each call is a Hatchet task that Brickie runs against an isolated sandbox pod in your Kubernetes cluster. Killing a sandbox stores a snapshot, a fork starts from it with the same files, and connect brings the original back from the same snapshot.

- [Quickstart](quickstart.md). A sandbox session on your laptop in a few minutes.
- [Install](install/README.md). Run the provider on Docker or Kubernetes.
- [Architecture](architecture.md). Turns, snapshots and the store.
- [Security](security.md). Trust boundaries, isolation and image verification.
- [Drivers](drivers.md)
- [Wire contract](wire-contract.md)
- [Extending](extending.md)
- [Contributing](contributing.md)
