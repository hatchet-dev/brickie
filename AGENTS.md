# Working on brickie

brickie runs agent sandboxes as Hatchet durable tasks. The README covers how to run it.

## Layout

- `cmd/brickie/`: cobra CLI, `worker` only (the sandbox provider)
- `internal/engine/`: turn contract, operator session, provider task, snapshot stores (local dir, object store)
- `internal/driver/local/`: Docker Sandboxes micro-VMs via the `sbx` CLI
- `internal/driver/kubernetes/`: one agent-sandbox `SandboxClaim` per sandbox, bound from a per-image warm pool, turns over pod exec
- `helm/brickie/`: the chart (provider Deployment as a Hatchet gRPC operator, agent-sandbox vendored under `charts/`); `Dockerfile` builds the provider image
- `sdks/typescript`: the client SDK; `examples/typescript`: the demo session
- `docker-compose.yml`: Hatchet (lite + Postgres); the provider runs on the host (sandboxd is host-only)
- `hack/run-demo.sh`: first-run setup + the demo session; also the end-to-end check

## Build and check

```bash
go build ./... && go vet ./...
cd sdks/typescript && pnpm install && pnpm typecheck
cd examples/typescript && pnpm install && pnpm typecheck
helm lint helm/brickie --set hatchet.token=t
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
- The sandbox `image` option means an sbx template on the local driver and a container image on the kubernetes driver. Turn timeouts, idle TTL, and runtime class are provider settings, not SDK settings.
- A sandbox is one live micro-VM or pod for the life of a session. The provider never recreates a sandbox behind a session: a missing or replaced sandbox is an error, and the caller creates a new one from a snapshot.
- `helm/brickie/values.schema.json` stays in sync with `values.yaml`: every new value gets a schema entry in the same change, and the schema keeps `additionalProperties: false` at the top level so typos fail at install time.
- One provider replica: turns on the same sandbox are serialized in-process. Add a Hatchet concurrency key on `input.sandboxId` before raising `replicaCount`.

## Environment notes

- When targeting Hatchet Cloud from a dev machine that has local hatchet mkcert setup, `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` first.
- `sbx` (Docker Sandboxes) needs `sbx login` and `sbx policy init` once; `hack/run-demo.sh` handles it.
- Claude spawned inside an sbx sandbox derives its working directory as the parent of the workspace mount. Always anchor prompts with the workspace path, as `internal/engine/runner.go` does.
