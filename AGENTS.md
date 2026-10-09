# Working on brickie

brickie runs agent sandboxes as Hatchet durable tasks. The README covers how to run it.

## Layout

- `cmd/brickie/`: cobra CLI, `worker` only (the sandbox provider)
- `internal/engine/`: turn contract, provider (a Hatchet gRPC operator, no worker mode), snapshot store
- `internal/driver/docker/`: sandbox lifecycle, snapshots, and turn dispatch on the local Docker engine; `sandboxd.go` injects the agent-sandbox runtime daemon into each container and runs commands through its gRPC ProcessService
- `internal/driver/fsops/`: the `$fs.*` file functions on the workspace mount through `os.Root`
- `sdks/typescript`: the client SDK; `examples/typescript`: the demo session
- `docker-compose.yml`: Hatchet lite v0.110.5 + Postgres with gRPC operators enabled; the provider runs on the host and talks to the local Docker engine
- `hack/run-demo.sh`: first-run setup + the demo session; also the end-to-end check

## Build and check

```bash
go build ./... && go vet ./...
cd sdks/typescript && pnpm install && pnpm typecheck
cd examples/typescript && pnpm install && pnpm typecheck
```

Build binaries to /tmp or use `go install`, do not commit binaries.

## Rules

- The engine and all execution logic are Go. The SDK stays thin: it triggers the `brickie-sandbox` task by name and never chooses a provider. Placement follows only from `HATCHET_CLIENT_TOKEN` being set or not.
- TypeScript: import the Hatchet SDK from `@hatchet-dev/typescript-sdk/v1` only, and build orchestration on v1 `DurableContext` primitives (`spawnChild`, `spawnChildren`, `sleepFor`, `waitForEvent`). Use pnpm, never npm or yarn.
- Task names must not contain colons. Hatchet action ids are `workflow:task`.
- Parallel children from a durable task must go through one bulk spawn (`spawnChildren`). Parallel single spawns trip non-determinism detection.
- Command turns return `{stdout, stderr, exitCode}` and never fail the task on a non-zero exit. The orchestrator decides what is fatal.
- Snapshot writes are keyed by run id so retries overwrite their own snapshot. Keep it that way: replay safety depends on it.
- No host environment variables reach sandboxes; Claude auth lives in the Docker Sandboxes agent kit. Never widen this implicitly.
- Do not add code comments except where the code cannot express a constraint. No comments explaining what the next line does.
- First version of anything new is 0.1.0.
- The sandbox `template` option is a container image. Idle TTL is a provider setting; a turn is bounded by the SDK's `timeoutMs` and the calling task's own timeout, never by a provider flag.
- A sandbox id is a run id or a caller-supplied id; the provider keeps a latest-snapshot pointer per sandbox and `$connect` resolves it. Never silently give a connect an empty workspace.

## Environment notes

- When targeting Hatchet Cloud from a dev machine that has local hatchet mkcert setup, `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` first.
- Commands run through `sandboxd` with `/workspace` as the working directory. Docker's init is PID 1 so orphans are reaped. The sandboxd image is pinned by digest in `internal/driver/docker/sandboxd.go`; upstream has no release tag yet.
