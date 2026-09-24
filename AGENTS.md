# Working on brickie

brickie runs agent sandboxes as Hatchet tasks. Read `docs/ARCHITECTURE.md` before changing anything structural. The README covers how to run it.

## Layout

- `cmd/brickie/`: cobra CLI (`dev`, plus hidden `worker`, `csi-node`, `injector`, `bootstrap-kind`, `bootstrap-aws`)
- `internal/awsboot/`: EKS bootstrap via aws-sdk-go-v2 (cluster, nodegroup, IAM, ECR, S3, gVisor and Kata installers, Helm SDK)
- `internal/engine/`: turn contract, provider task, snapshot store, conversation transcript
- `internal/driver/`: `local` (Docker Sandboxes via `sbx` CLI) and `kubernetes` (one agent-sandbox `SandboxClaim` per session plus pod exec)
- `internal/overlay/`: overlayfs layer store, mount/checkpoint/restore, object store offload
- `internal/csi/`: CSI node plugin on top of `internal/overlay`
- `internal/injector/`: credential injector sidecar (sealed secrets, host allowlists, hatchet proxy)
- `sdks/typescript`: the SDK, `examples/typescript`: the demo session
- `helm/brickie/`: the chart, agent-sandbox vendored under `charts/`
- `hack/`: `run-demo.sh` (local guided setup), `test.sh` (e2e), `apply-runtime` (install gvisor or kata on the current cluster)

## Build and check

```bash
go build ./... && go vet ./...
pnpm install && (cd sdks/typescript && pnpm typecheck) && (cd examples/typescript && pnpm typecheck)
helm lint helm/brickie --set hatchet.token=t
```

Build binaries to /tmp or use `go install`, do not commit binaries.

## Rules

- The engine and all execution logic are Go. The SDK stays thin: it triggers the `brickie-sandbox` task by name and never chooses a driver or provider. Placement follows only from `HATCHET_CLIENT_TOKEN` being set or not.
- A sandbox is one live micro-VM or pod for the life of an agent session. `$create` binds it, `$command` and `$prompt` run inside it, `$destroy` releases it. The provider never recreates a sandbox behind a session: a missing or replaced sandbox is an error, and the caller creates a new one from a snapshot.
- Sandbox ids come from the SDK (`<run id>-<n>`) so a replayed durable task lands in the same sandboxes. Every turn requires `sandboxId`; only `$create` accepts `snapshotId`.
- TypeScript: import the Hatchet SDK from `@hatchet-dev/typescript-sdk/v1` only, and build orchestration on v1 `DurableContext` primitives. Use pnpm, never npm or yarn.
- Task names must not contain colons. Hatchet action ids are `workflow:task`.
- Command turns return `{stdout, stderr, exitCode}` and never fail the task on a non-zero exit. The orchestrator decides what is fatal.
- Snapshot writes are keyed by run id so a retried turn overwrites its own snapshot. Turns on a live sandbox are at-least-once; keep them safe to re-run.
- Linux-only syscalls (mounts, syncfs) live behind `//go:build linux` with stubs for other platforms. The repo must build on macOS.
- Do not add code comments except where the code cannot express a constraint. No comments explaining what the next line does.
- First version of anything new is 0.1.0.
- `helm/brickie/values.schema.json` must always exist and stay in sync with `values.yaml`: every new value gets a schema entry in the same change, and the schema keeps `additionalProperties: false` at the top level so typos fail at install time.
- The sandbox `template` option means an sbx template on the local driver and a container image on the kubernetes driver. Turn timeouts, idle TTL, runtime, and default images are provider settings, not SDK settings.

## Environment notes

- `sbx` (Docker Sandboxes) needs `sbx login` and `sbx policy init` once; `hack/run-demo.sh` and `brickie dev` handle it.
- When targeting Hatchet Cloud from a dev machine that has local hatchet mkcert setup, `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` first.
- Claude spawned by the Agent SDK inside an sbx sandbox derives its working directory as the parent of the workspace mount. Always anchor prompts with the workspace path, as `internal/engine/runner.go` does.
- Kata (Cloud Hypervisor) needs KVM: EKS `.metal` amd64 nodes only. kind and gVisor clusters need nothing special.
