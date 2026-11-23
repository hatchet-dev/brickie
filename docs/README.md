# Brickie

Run sandboxes with filesystem snapshot/restore in your own Kubernetes cluster or cloud account.

Sandboxes are built on top of [agent-sandbox](https://agent-sandbox.sigs.k8s.io/) with custom snapshot/restore implemented on top of object storage. They are designed to be called from [`@hatchet-dev/sandbox`](https://www.npmjs.com/package/@hatchet-dev/sandbox) and support the ability to take a [coordinated snapshot](https://hatchet.run/blog/agent-statefulness) of agent state. This means that every sandbox call (`create`, `exec`, `kill`, etc.) is a retryable and observable [Hatchet](https://hatchet.run?utm_source=brickie-github&utm_medium=referral&utm_campaign=brickie-launch&utm_content=readme) task.

Each sandbox session is a container with a shell and a workspace and runs in your Kubernetes cluster with flexible runtime support, including [Kata](https://katacontainers.io/), [gVisor](https://gvisor.dev/) and [AWS Nitro](https://docs.aws.amazon.com/eks/latest/userguide/isolated-sandbox.html). Every call that changes the workspace produces a snapshot, which can be used to fork a new sandbox or resume an existing session.

> [!WARNING]
> **Beta.** Brickie is in beta. Flags, Helm chart values and the wire contract between the SDK and the provider may change before 1.0. Changes are listed in the [changelog](../CHANGELOG.md).

The provider runs locally on your laptop with Docker for local testing or in your own Kubernetes cluster for production workloads. It makes an egress connection to a running Hatchet instance using the same mechanism a Hatchet worker would with a tenant API token.

The Brickie provider and its components always run in FIPS 140-3 mode.

## Sandbox lifecycle

The basic sandbox lifecycle events are `create`, `exec`, `kill`, and `connect`. Each call is a Hatchet task that Brickie runs against an isolated sandbox. Killing a sandbox removes it and keeps its snapshots, a fork starts from any snapshot with the same files, and connect brings the original back from its latest one.

- [Quickstart](quickstart.md). A sandbox session on your laptop in a few minutes.
- [Install](install/README.md). Run the provider on Docker or Kubernetes.
- [Architecture](architecture.md). Turns, snapshots and the store.
- [Security](security.md). Trust boundaries, isolation and image verification.
- [Drivers](drivers.md). Docker for local testing, Kubernetes for production.
- [Wire contract](wire-contract.md). What the SDK sends and gets back.
- [Extending](extending.md). Write your own driver.
- [Contributing](contributing.md). How to build, test and send changes.
