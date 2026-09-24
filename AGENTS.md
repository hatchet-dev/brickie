# Working on brickie

brickie runs agent sandboxes as Hatchet durable tasks. Read `docs/ARCHITECTURE.md` before changing anything structural. The README covers how to run it.

## Layout

- `cmd/brickie/`: cobra CLI (`dev`, plus hidden `worker`, `csi-node`, `injector`, `bootstrap-kind`, `bootstrap-aws`)
- `internal/awsboot/`: EKS bootstrap via aws-sdk-go-v2 (cluster, nodegroup, IAM, ECR, S3, gVisor installer)
- `internal/engine/`: turn contract, durable task, snapshot store
- `internal/driver/`: `local` (Docker Sandboxes via `sbx` CLI) and `kubernetes` (agent-sandbox CRs plus pod exec)
- `internal/overlay/`: overlayfs layer store, mount/checkpoint/fork, object store offload
- `internal/csi/`: CSI node plugin on top of `internal/overlay`
- `internal/injector/`: credential injector sidecar (sealed secrets, host allowlists, hatchet proxy)
- `sdks/{typescript,python,go}`: thin clients, `examples/{typescript,python,go}`: runnable sessions
- `helm/brickie/`: the chart, CRDs under `crds/`, agent-sandbox vendored under `charts/`

## Build and check

```bash
go build ./... && go vet ./...
cd sdks/typescript && pnpm install && pnpm typecheck
cd examples/typescript && pnpm install && pnpm typecheck
python3 -m py_compile sdks/python/hatchet_sandbox/__init__.py
helm lint helm/brickie --set hatchet.token=t
```

Build binaries to /tmp or use `go install`, do not commit binaries.

## Rules

- The engine and all execution logic are Go. SDKs stay thin: they trigger the `brickie-sandbox` task by name and never choose a driver or provider. Placement follows only from `HATCHET_CLIENT_TOKEN` being set or not.
- TypeScript: import the Hatchet SDK from `@hatchet-dev/typescript-sdk/v1` only, and build orchestration on v1 `DurableContext` primitives (`spawnChild`, `spawnChildren`, `sleepFor`, `waitForEvent`). Use pnpm, never npm or yarn.
- Task names must not contain colons. Hatchet action ids are `workflow:task`.
- Parallel children from a durable task must go through one bulk spawn (`spawnChildren`, `RunMany`, `aio_run_many`). Parallel single spawns trip non-determinism detection.
- Command turns return `{stdout, stderr, exitCode}` and never fail the task on a non-zero exit. The orchestrator decides what is fatal.
- Snapshot writes are keyed by run id so retries overwrite their own snapshot. Keep it that way: replay safety depends on it.
- Linux-only syscalls (mounts, syncfs) live behind `//go:build linux` with stubs for other platforms. The repo must build on macOS.
- Do not add code comments except where the code cannot express a constraint. No comments explaining what the next line does.
- First version of anything new is 0.1.0.
- `helm/brickie/values.schema.json` must always exist and stay in sync with `values.yaml`: every new value gets a schema entry in the same change, and the schema keeps `additionalProperties: false` at the top level so typos fail at install time.
- The sandbox `image` option means an sbx template on the local driver and a container image on the kubernetes driver. Turn timeouts and default images are provider settings, not SDK settings.

## Environment notes

- `sbx` (Docker Sandboxes) needs `sbx login` and `sbx policy init` once; `brickie dev` handles it.
- When targeting Hatchet Cloud from a dev machine that has local hatchet mkcert setup, `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` first.
- Claude spawned by the Agent SDK inside an sbx sandbox derives its working directory as the parent of the workspace mount. Always anchor prompts with the workspace path, as `internal/engine/runner.go` does.
