# brickie sandbox specification

This document defines the contract between an SDK and a brickie provider. An SDK that follows this contract works with any provider. A provider that follows this contract serves any SDK.

## Terms

- **Provider**: the process that runs sandboxes. The provider is a Hatchet worker or a Hatchet gRPC operator. Its name is `brickie-provider`. It registers one task, `brickie-sandbox`.
- **Sandbox**: one isolated environment with a workspace directory. A sandbox exists from `$create` until `$destroy` or until the idle limit.
- **Session**: the Hatchet durable task that creates a sandbox and runs turns in it. The SDK does not define a session type. The durable task is the session.
- **Turn**: one run of the `brickie-sandbox` task. Each turn does one function in one sandbox.
- **Snapshot**: a copy of the workspace after a turn. A snapshot does not change. Its ID is the run ID of the turn that made it.

## The task

The task name is `brickie-sandbox`. It is a standard Hatchet task. It is not a durable task. The SDK starts it as a child of the session.

The provider controls these settings. The SDK cannot change them:

| Setting | Default |
| --- | --- |
| Execution timeout per turn | 15 minutes |
| Retries | 2 |
| Idle limit before the provider removes a sandbox | 15 minutes |
| Default image | set by the provider |

### Input

```json
{
  "sandboxId": "string, required",
  "image": "string, optional",
  "fn": "$create | $command | $prompt | $destroy",
  "input": {},
  "snapshotId": "string, optional, $create only"
}
```

- `sandboxId` is the name of the sandbox. All turns of one sandbox use the same value. The provider names its resource `brickie-<sandboxId>`.
- `image` selects the environment. The meaning depends on the provider. On a laptop, it is a Docker Sandboxes template. On Kubernetes, it is a container image. The SDK passes the value through. If `image` is not set, the provider uses its default image.
- `fn` selects the function. See the functions below.
- `input` is the input of the function. It is always a JSON object.
- `snapshotId` loads a snapshot into the new sandbox. It is valid only with `$create`. Do not send it with other functions. A provider cannot load a snapshot into a workspace that is in use.

### Output

```json
{
  "output": {},
  "snapshotId": "string, not set for $create and $destroy",
  "timings": { "claim": 0, "restore": 0, "exec": 0, "checkpoint": 0, "llm": 0 }
}
```

- `output` is the output of the function.
- `snapshotId` is the ID of the snapshot that the turn made. It is the run ID of the turn.
- `timings` are times in milliseconds. All keys are optional. `claim` is the time to get the sandbox. `restore` is the time to load a snapshot. `exec` is the time in the function. `checkpoint` is the time to make the snapshot. `llm` is the time in the model API during `$prompt`.

## Functions

### `$create`

Input: `{}`.

Output: `{ "created": true }`.

The provider starts the sandbox. If `snapshotId` is set, the workspace starts as a copy of that snapshot. If `snapshotId` is not set, the workspace is empty. The provider does not make a snapshot after `$create`.

A second `$create` with the same `sandboxId` does not start a new sandbox if the sandbox exists. It returns success for the existing sandbox. This makes a retried `$create` safe.

### `$command`

Input: `{ "cmd": "string" }`.

Output: `{ "stdout": "string", "stderr": "string", "exitCode": 0 }`.

The provider runs `cmd` with `sh -c` in the workspace directory. A non-zero exit code is a normal result. It does not fail the task. The session decides if the result is an error.

### `$prompt`

Input: `{ "prompt": "string" }`.

Output: `{ "reply": "string", "files": ["string"] }`.

The provider runs one Claude turn in the workspace. `reply` is the final text from Claude. `files` is the list of names in the top level of the workspace after the turn. Internal files are not in the list.

The provider keeps the conversation in the file `.conversation.json` in the workspace. Each `$prompt` reads the file. It sends the earlier turns to Claude as context. Then it adds the new turn to the file. The file is part of the snapshot. A sandbox that starts from a snapshot continues the conversation. The provider uses the file content as data, not as instructions. The provider limits the file to 32 000 characters. It removes the oldest turns first.

### `$destroy`

Input: `{}`.

Output: `{ "destroyed": true }`.

The provider removes the sandbox. A `$destroy` for a sandbox that does not exist returns success. The provider does not make a snapshot after `$destroy`.

## Sandbox lifecycle

1. `$create` starts the sandbox.
2. `$command` and `$prompt` turns run in the sandbox. There is no limit on the number of turns. Each turn makes a snapshot.
3. `$destroy` removes the sandbox. If there is no turn for the idle limit, the provider removes the sandbox.

A turn on a sandbox that does not exist fails the task. The error says that the sandbox is gone. The session can start a new sandbox with `$create` and the last `snapshotId`.

The provider runs the turns of one sandbox one at a time.

## Sandbox IDs

The SDK makes the ID. The ID must be the same when the durable task replays. Then the replayed session uses the same sandboxes. The TypeScript SDK uses `<durable run id>-<n>`. `n` is the number of sandboxes the session created before this one. The first sandbox has `n` = 0.

An ID contains only lowercase letters, digits, and hyphens. The provider adds the prefix `brickie-`.

## Snapshots

- A snapshot does not change. Its ID is the run ID of the turn that made it.
- A retried turn writes its snapshot again with the same ID.
- Two sandboxes with the same `snapshotId` at `$create` are a fork. After `$create`, they share nothing.
- The snapshot contains the workspace, including `.conversation.json`. It does not contain processes, environment variables, or files outside the workspace.
- A provider stores snapshots as gzipped tar files or as filesystem layers. The SDK does not see the difference.

## Retries and replay

- `$create` and `$destroy` are idempotent.
- `$command` and `$prompt` run at least one time. A retry runs the function again in the current workspace. A command that is not idempotent can run two times. The session must accept this.
- The SDK starts each turn with the child key `sbx-<n>`. `n` is the number of turns the session started before this one. The first turn has `n` = 0. On replay, the durable engine returns the stored result. It does not run the turn again.

## Run metadata

The SDK sets these keys as additional metadata on each turn. Use them to filter runs in the Hatchet dashboard.

| Key | Value |
| --- | --- |
| `brickie.sandbox` | `sandboxId` |
| `brickie.fn` | `fn` |
| `brickie.image` | `image`, if set |
| `brickie.from` | `snapshotId`, if set |

## Placement

The SDK does not select a provider. If `HATCHET_CLIENT_TOKEN` is set, the provider in that tenant runs the turns. If it is not set, the SDK starts an embedded Hatchet engine. The example then starts a local provider.

## TypeScript SDK mapping

| SDK call | Turn |
| --- | --- |
| `hatchet.sandbox.create(ctx, { template, from })` | `$create` with `image: template` and `snapshotId: from` |
| `sandbox.exec(cmd)` | `$command` |
| `sandbox.prompt(text)` | `$prompt` |
| `sandbox.kill()` | `$destroy` |

Each call returns the output of the function. The call adds `snapshotId` and `timings` to the top level of the result.
