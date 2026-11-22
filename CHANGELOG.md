# Changelog

All notable changes to the brickie provider. The TypeScript SDK, `@hatchet-dev/sandbox`, lives in [hatchet-dev/hatchet](https://github.com/hatchet-dev/hatchet/tree/main/sdks/typescript-sandbox) with its own changelog.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Each release reads its notes from the matching section here.

## [Unreleased]

### Added

- Docs site at [hatchet-dev.github.io/brickie](https://hatchet-dev.github.io/brickie), built from `docs/` and deployed on every push to main. Every page is also served as Markdown, with `llms.txt` and `llms-full.txt` for agents.
- `SECURITY.md` with the vulnerability reporting policy.
- `CONTRIBUTING.md`, `AI_POLICY.md`, issue and pull request templates, and the vouch workflows that close pull requests from authors not in `.github/VOUCHED.td`.

## [0.1.7] - 2026-11-17

### Added

- `--sandboxd-image` (`BRICKIE_SANDBOXD_IMAGE`) picks the image the sandboxd binary is taken from.
- `--sandbox-init-containers` (`BRICKIE_SANDBOX_INIT_CONTAINERS`): a JSON list of Kubernetes containers run as init containers in every sandbox pod, before sandboxd and the sandbox container, under the pod's runtime class. For operator checks such as a FIPS verification script.
- `--log-format` (`BRICKIE_LOG_FORMAT`): `json`, the default, one object per line with `time`, `level`, `msg` and fields such as `run` for turn logs; or `text` for a terminal, which `hack/run-demo.sh` uses. All lines go to stdout with an explicit level, so log collectors stop reporting startup and shutdown messages as errors. The Hatchet client's own logs follow the same format.
- `ghcr.io/hatchet-dev/sandboxd` and `ghcr.io/hatchet-dev/agent-sandbox-controller` carry FIPS 140-3 builds of the upstream agent-sandbox release under its version tags, from v1.0.8 on, published daily.
- Every published image is signed with cosign keyless signing and carries a syft SPDX SBOM attestation per platform. `docs/SECURITY.md` shows how to verify.

### Changed

- sandboxd comes from `sandboxd:v1.0.8` by default instead of a digest mirror of upstream's staging image.

### Fixed

- Stopping the provider with SIGTERM logged `Error: context canceled` followed by the full command usage. It now logs one "shutting down" line and exits 0.

## [0.1.6] - 2026-11-17

### Fixed

- Restoring a snapshot into a sandbox that runs as a non-root user failed with `tar: .: Cannot utime: Operation not permitted` when the image ships GNU tar. The restore now skips the workspace directory entry itself, so `/workspace` is never modified and only its contents are extracted. Existing snapshots are unaffected.

## [0.1.5] - 2026-11-10

### Added

- `--runtime` accepts any RuntimeClass name on the cluster, and the alias `nitro` for EKS `isolated-sandbox`. `auto` tries `kata-clh`, `gvisor`, then `isolated-sandbox`.

### Removed

- The TypeScript SDK sources, build and npm publish. `@hatchet-dev/sandbox` is developed and released from `hatchet-dev/hatchet`; the examples and the end-to-end test suite here use the published package.

## [0.1.4] - 2026-10-30

### Added

- `--sandbox-node-selector` and `--sandbox-tolerations` place sandbox pods on tainted or labelled nodes, whatever the runtime class. Tolerations are a JSON list of Kubernetes tolerations.

## [0.1.3] - 2026-10-30

### Added

- `$create` with `inheritMetadata` starts a fork from the metadata of the sandbox that made its snapshot; pairs given with the create take precedence.
- `$delete`: stops a sandbox and removes everything the store holds for it, metadata first so a concurrent `connect` finds no sandbox rather than an empty one. Idempotent, so a retry finishes a partial delete.

### Changed

- Snapshot ids are `<sandboxId>/<runId>` and stored results live under `results/<sandboxId>/`, so a sandbox's objects share one prefix and `$delete` can find all of them. Snapshot ids from earlier releases are refused, so a sandbox created before this release cannot be connected or forked any more and its objects are pruned after 7 days. A turn in flight across the upgrade re-runs once if Hatchet retries it.

## [0.1.2] - 2026-10-28

### Changed

- Kubernetes sandbox pods request 50Mi of ephemeral storage instead of the full `--sandbox-disk`, which stays the limit, so a sandbox no longer reserves 10Gi of node disk it does not use.

## [0.1.1] - 2026-10-27

### Added

- Kubernetes driver (`--driver kubernetes`): every sandbox is an [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) claim bound from a pre-started warm pool per image, under the `kata-clh` or `gvisor` runtime class when the cluster has one (`--runtime`), with a NetworkPolicy that admits only the provider (`--provider-namespace`, `--provider-selector`) and bounds egress (`--sandbox-egress`), and CPU, memory, and disk limits per sandbox (`--sandbox-cpu`, `--sandbox-memory`, `--sandbox-disk`).
- Mutual TLS between the provider and every Kubernetes sandbox: `brickie sandbox-tls` runs from the provider's own image (`--provider-image`) beside sandboxd in each pod and terminates TLS 1.3 on the pod IP, with a CA the provider keeps in its `brickie-sandboxd` Secret and a server certificate it issues into `brickie-sandbox-tls` and renews before expiry.
- `hack/e2e-kind.sh` runs both demos against the Kubernetes driver on a local kind cluster.
- Helm chart `brickie` in [hatchet-charts](https://github.com/hatchet-dev/hatchet-charts): the provider, the agent-sandbox controller and Kata Containers as bundled subcharts, an optional gVisor installer, and an egress allow-list for sandbox pods.
- The container image `ghcr.io/hatchet-dev/brickie`, for linux/amd64 and linux/arm64, built with the certified Go Cryptographic Module and published with every release.
- `docs/SECURITY.md` describes the trust boundaries and the control that holds each one.

### Changed

- `--store` replaces `--snapshot-dir` and `--object-store`: snapshots, sandbox metadata, and staged files share one bucket (`s3://`, `gs://`, `azblob://`, or `file://`), `~/.brickie/store` by default, and cleanup touches only the provider's own objects.
- Both drivers run every turn through sandboxd: reads, writes, and listings over its REST API, `mkdir`, `rename`, `copy`, and `remove` as confined commands inside the sandbox, snapshots as `tar` inside the sandbox streamed to the store. The provider never touches sandbox files itself.
- A turn holds its sandbox for as long as it runs, however long, and the idle window starts when it ends.
- A turn that stored its snapshot records its result before publishing the latest pointer; a retry of the same run returns the stored result instead of running the command again, and publishes its snapshot only if no later turn has published since.
- Store cleanup keeps the metadata and current snapshot of any sandbox whose latest pointer is younger than 7 days.
- `$fs.presign` with a `stagedKey` signs a fresh read URL for a file the sandbox already staged.
- A restore never extracts the workspace marker, so a restore that fails part way is retried on the next connect instead of reporting an empty workspace.
- The sandboxd image is pulled from `ghcr.io/hatchet-dev/sandboxd`, a digest-preserving mirror of the pinned upstream image.
- Command stdout and stderr are capped at 1 MiB each and marked truncated beyond that; snapshots larger than `--max-snapshot-bytes` (1 GiB) fail the turn.

## [0.1.0] - 2026-10-27

### Added

- `brickie worker`, the sandbox provider, running as a Hatchet gRPC operator for the `brickie-sandbox` task.
- Docker driver: one container per sandbox on the local engine, `sandboxd` injected as the process, commands over gRPC with timeouts that kill the whole process group.
- Workspace snapshots after every mutating call, keyed by run id, with a latest-snapshot pointer per sandbox so `connect` resumes a sandbox by id and `create({ from })` forks any snapshot.
- File functions on the workspace: read, write, list, stat, mkdir, rename, copy, remove, download, upload; files larger than `--max-file-bytes` go through `--object-store`.
- Idle expiry per sandbox with `--sandbox-idle-ttl` and a per-create `idleTimeoutMs` bounded by `--max-sandbox-idle-ttl`.
- Sandbox metadata and `list` with metadata filters.
- Release binaries for Linux and macOS on amd64 and arm64, macOS binaries signed and notarized.
- `brickie version`, printing the release version, commit, build date, Go version, platform, and FIPS status.
- Release binaries are linked against the CMVP-certified Go Cryptographic Module v1.0.0 with FIPS 140-3 mode on by default; the release fails if a binary is not.
