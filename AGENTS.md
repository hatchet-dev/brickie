# Working on brickie

brickie runs agent sandboxes as Hatchet durable tasks. The README covers how to run it.

## Layout

- `cmd/brickie/`: cobra CLI: `worker` (the sandbox provider) and `sandbox-tls` (the TLS proxy that runs beside sandboxd in every Kubernetes sandbox pod)
- `internal/engine/`: turn contract, provider (a Hatchet gRPC operator, no worker mode), the store (snapshots, metadata, staged files in one gocloud bucket)
- `internal/sandboxd/`: client for the agent-sandbox runtime daemon that is PID 1 in every sandbox; commands over gRPC, files over REST, bearer token on both, mutual TLS when the driver passes a config
- `internal/sandboxtls/`: the CA the provider holds, the certificates it issues, and the proxy `sandbox-tls` runs
- `internal/driver/`: the turn body both drivers share (restore, `$fs.*`, `$command`, checkpoint); `docker/` runs sandboxes as containers on the local engine, `kubernetes/` as agent-sandbox claims bound from per-image warm pools
- `examples/typescript`: the demo session and the end-to-end test suite, both on the published `@hatchet-dev/sandbox` SDK, which lives in `hatchet-dev/hatchet` under `sdks/typescript-sandbox`
- `docker-compose.yml`: Hatchet lite v0.110.5 + Postgres with gRPC operators enabled; the provider runs on the host and talks to the local Docker engine
- `hack/run-demo.sh`: first-run setup + the demo session on Docker; `hack/e2e-kind.sh`: the same demos against the Kubernetes driver on a kind cluster
- `docs/`: the Fumadocs site at hatchet-dev.github.io/brickie, static export deployed by `.github/workflows/docs.yaml` on push to main. Pages are MDX in `docs/content`, short and in full sentences like the README. `docs/content/security.mdx` is the security model, update it when a boundary moves. The flag table in `docs/content/install/configuration.mdx` mirrors `registerFlags` in `cmd/brickie/worker.go`, change both.
- `Dockerfile`: the provider image, `ghcr.io/hatchet-dev/brickie`, which also runs as the TLS proxy in every sandbox pod. The Helm chart that deploys it lives in `hatchet-charts` as `charts/brickie`; a release here needs its `appVersion` bumped there.

## Build and check

```bash
go build ./... && go vet ./...
pnpm install && pnpm -r typecheck
pnpm --filter brickie-docs build
```

Build binaries to /tmp or use `go install`, do not commit binaries.

CI runs both demos end to end on every pull request: `.github/workflows/e2e-docker.yaml` on the Docker driver (`hack/run-demo.sh`) and `.github/workflows/e2e-kind.yaml` on the Kubernetes driver (`hack/e2e-kind.sh` on a kind cluster). `.github/workflows/e2e-typescript.yaml` runs the test suite (`examples/typescript/test`, `pnpm test`) against a provider started with `hack/run-demo.sh up`; the demos stay simple and the concept checks live in that suite. Each first runs `hack/ensure-sandboxd-image.sh`, which builds the pinned sandboxd image locally when its tag is not on GHCR yet; pull requests never publish images. The kind node pulls that image without credentials, so the package has to be public.

## Rules

- The engine and all execution logic are Go. The SDK stays thin: it triggers the `brickie-sandbox` task by name and never chooses a provider. A change to the turn contract here needs a matching SDK change in `hatchet-dev/hatchet`, kept backwards compatible.
- TypeScript: import the Hatchet SDK from `@hatchet-dev/typescript-sdk/v1` only, and build orchestration on v1 `DurableContext` primitives (`spawnChild`, `spawnChildren`, `sleepFor`, `waitForEvent`). Use pnpm, never npm or yarn.
- Task names must not contain colons. Hatchet action ids are `workflow:task`.
- Parallel children from a durable task must go through one bulk spawn (`spawnChildren`). Parallel single spawns trip non-determinism detection.
- Command turns return `{stdout, stderr, exitCode}` and never fail the task on a non-zero exit. The orchestrator decides what is fatal.
- Snapshot writes are keyed by run id so retries overwrite their own snapshot. Keep it that way: replay safety depends on it.
- No host environment variables reach sandboxes; Claude auth lives in the Docker Sandboxes agent kit. Never widen this implicitly.
- Log through `log/slog` only; `--log-format` picks JSON (default) or text and configures the Hatchet client's logger too. Never write to stderr directly.
- Do not add code comments except where the code cannot express a constraint. No comments explaining what the next line does.
- First version of anything new is 0.1.0.
- Everything is FIPS 140-3, there is no non-FIPS variant. `brickie` builds with `GOFIPS140=v1.0.0` (Dockerfile, `.goreleaser.yml`) and `//go:debug fips140=on` in `cmd/brickie/main.go`, gated by `hack/check-fips.sh`; do not remove either. `agent-sandbox-controller` and `sandboxd` are our FIPS builds of the upstream agent-sandbox release, from `hack/agent-sandbox-fips.Dockerfile` via `fips-images.yaml`, daily. A published tag is never overwritten; a recipe fix ships under the next upstream release or a new tag. Every image is signed keyless with cosign and carries a syft SPDX SBOM attestation through `.github/actions/sign-image`. kata-deploy has no FIPS variant: nothing in it does cryptography. The default sandbox image `alpine:3` is not FIPS; brickie runs no cryptography inside the sandbox image.
- `CHANGELOG.md` follows Keep a Changelog. A change goes under `Unreleased`; a release turns that into a `## [version] - date` section, which the release workflow reads and refuses to ship without. Never edit a section that has been released.
- The sandbox `template` option is a container image. Idle TTL is a provider setting; a turn is bounded by the SDK's `timeoutMs` and the calling task's own timeout, never by a provider flag.
- A sandbox id is a run id or a caller-supplied id; the provider keeps a latest-snapshot pointer per sandbox and `$connect` resolves it. Never silently give a connect an empty workspace.

## Environment notes

- When targeting Hatchet Cloud from a dev machine that has local hatchet mkcert setup, `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` first.
- Commands run through `sandboxd` with `/workspace` as the working directory. On Docker, Docker's init is PID 1 so orphans are reaped. The sandboxd image is `ghcr.io/hatchet-dev/sandboxd:<tag>`, our FIPS build, with the tag in `internal/sandboxd/sandboxd.go`. It must match the agent-sandbox pin in `go.mod` and `agent-sandbox.image.tag` in the chart. `--sandboxd-image` replaces it.
- On Kubernetes, sandboxd listens on loopback and the provider dials the pod's `sandbox-tls` proxy on 9443 and 8443 with a client certificate. The Kubernetes driver therefore needs `--provider-image`, the image it runs from, and `get`, `create`, `update` on secrets in both namespaces. The Docker driver stays plaintext on loopback.
- The provider never touches sandbox files itself: reads, writes, and listings go through the sandboxd REST API; `mkdir`, `rename`, `copy`, and `remove` are commands inside the sandbox behind the confinement check in `internal/driver/fsops`; snapshots are `tar` runs inside the sandbox streamed to the store. Keep it that way; it is what makes the two drivers one code path.
- Files over `--max-file-bytes` travel between the SDK and the provider through a transport the provider owns: today the store's presigned URLs (`fsops.Staging`, `putStaged`/`getStaged` in the SDK). A Hatchet payload-by-reference transport is added beside it, chosen per provider, never in place of it. Both transports must keep passing `hack/run-demo.sh fs`, and switching between them must not change the sandbox-facing contract or the store layout.
