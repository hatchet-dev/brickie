# Extending

Write a driver for another sandbox backend.

A driver is the one part of Brickie that knows how to start a sandbox and how to reach it. Everything else is shared.

## The interface

```go
// internal/engine/types.go
type Driver interface {
	RunTurn(ctx context.Context, req TurnRequest) (TurnResponse, error)
	Live(ctx context.Context) map[string]time.Time
}
```

`TurnRequest` gives you everything about one turn - the sandbox ID and name, the image, the function and its JSON input, a reader with the snapshot to unpack into a fresh sandbox, a writer that takes the workspace tar when the turn is done, the idle TTL, the metadata, and a log callback.

`TurnResponse` is what you hand back to the SDK - the function's JSON output, whether a snapshot was taken, and how long each phase took.

`Live` returns the sandboxes your driver has running right now, with the time each one last ran a turn.

## What the engine already does

Before a turn ever reaches your driver, the engine has done a lot of the work. It answers `$list`, `$delete` and `$fs.presign` from the store on its own. It checks the sandbox ID. It makes sure only one turn runs on a sandbox at a time. It streams snapshots to and from the store, stores results so that retries return the same answer, and moves the `latest` pointer. Your driver never sees two turns on the same sandbox at once.

## What your driver does

1. On `$destroy`, remove the sandbox.
2. On `$create` and `$connect`, find the sandbox or start a new one, and note whether it is fresh. A fresh sandbox needs the snapshot unpacked into it.
3. On any other function, fail if the sandbox is not running.
4. Get a `*sandboxd.Client` for the sandbox and call `driver.Run(ctx, client, req, fresh, timings, driver.Options{MaxFileBytes, Staging})`. That one call unpacks the snapshot, runs the function and streams the new snapshot out. Fill in the `claim`, `restore`, `exec` and `checkpoint` timings as you go.

If your backend can run [`sandboxd`](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/packages/sandboxd/USER_GUIDE.md) as the process inside the sandbox, that is the whole job. Look at `internal/driver/docker` for the smallest working example and at `internal/driver/kubernetes` for the full one with warm pools, mutual TLS and runtime classes.

If your backend cannot run `sandboxd`, you need your own way to run commands and move files, and it has to keep the same promises. Read the [security page](security.md) first. Every byte that comes out of a sandbox is hostile, every path is checked twice, and the provider never follows a redirect from a sandbox.

## Wire it in

Drivers are picked by a `switch` on `--driver` in `cmd/brickie/worker.go`. Add a case there that builds your driver from its own flags. Register those flags in `registerFlags`, each with a `BRICKIE_*` environment variable. The driver you build goes to `engine.RunOperatorProvider`, and that is it.

## Keep the contract

The SDK stays thin. It starts the `brickie-sandbox` task by name and never knows which provider or driver is behind it. A new driver must not change the [wire contract](wire-contract.md).

## Testing

When it works, run both demos against your driver. `hack/run-demo.sh` and `hack/run-demo.sh fs` on Docker, and the e2e suite in `examples/typescript/test` with `pnpm test`.
