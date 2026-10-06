# brickie

brickie runs agent sandboxes as Hatchet tasks.

A sandbox is a Docker container, or a Docker Sandboxes micro-VM. Each agent turn runs in the sandbox as a Hatchet task. Each turn makes a snapshot of the workspace. A snapshot does not change. Any snapshot can start a new sandbox. A session can continue or fork from any turn. The files and the conversation are in the snapshot.

## Requirements

- Docker.
- Node and pnpm.
- For the `sbx` driver: Docker Desktop on macOS and Homebrew. The setup script uses Homebrew to install the `sbx` CLI.

## Run the demo

Run this command:

```bash
hack/run-demo.sh
```

The script does the setup step by step. Before each step, it shows the command and asks for approval. The script:

1. Installs and configures Docker Sandboxes, with `--driver sbx`.
2. Starts Hatchet with Docker Compose.
3. Makes a Hatchet token and writes it to `.env`.
4. Builds the sandbox provider in a Go container.
5. Starts the sandbox provider.
6. Starts the demo session.

The script is safe to run again at any time. It skips the steps that are complete.

The demo is one durable task with the name `agent-session`. It shows each SDK function once:

1. `create` starts a sandbox with the ID `demo-<random>` and metadata. `list` finds it by that metadata.
2. `exec` runs a shell command and makes a snapshot.
3. `files.write`, `files.read`, and `files.list` work with files.
4. `create` with `from` forks the snapshot into a second sandbox.
5. `kill` removes both sandboxes.
6. `connect` starts the sandbox again from its latest snapshot.

Each step prints the snapshot ID and the time for each phase. Open [http://localhost:8888](http://localhost:8888) to see the task and its turns in the Hatchet dashboard. Log in with `admin@example.com` and `Admin123!!`. The demo code is in `examples/typescript/demo.ts`. `hack/run-demo.sh fs` runs the file demo in `fs-demo.ts` instead.

## TypeScript SDK

A sandbox is one container or micro-VM. Any code with a Hatchet client can create a sandbox. In a durable task, each sandbox call is in the event log of that task. The durable task is the agent session.

```ts
import '@hatchet/sandbox';

const session = hatchet.durableTask({
  name: 'agent-session',
  executionTimeout: '1h',
  fn: async (input) => {
    const sandbox = await hatchet.sandbox.create();
    await sandbox.files.write('haiku.txt', 'an old silent pond');
    const haiku = await sandbox.exec('cat haiku.txt');

    const fork = await hatchet.sandbox.create({ from: haiku.snapshotId });
    await fork.exec('echo "a frog jumps in" >> haiku.txt');

    await sandbox.kill();
    await fork.kill();
    return { haiku: haiku.stdout };
  },
});
```

### Create a sandbox

- `hatchet.sandbox.create(options)` starts a sandbox. This is the cold start.
- `options.id` sets the sandbox ID. Use lowercase letters, digits, and hyphens. Without it, the provider makes an ID. The ID must be new on the provider.
- `options.template` selects the image. On the `docker` driver, it is a container image. The default is `alpine:3`. On the `sbx` driver, it is a Docker Sandboxes template. The default is `claude`.
- `options.metadata` tags the sandbox with key-value pairs. `options.idleTimeoutMs` sets how long it may stay idle. The provider caps it with `--max-sandbox-idle-ttl`.
- `hatchet.sandbox.connect(id)` continues a sandbox. If the sandbox is live, the calls go to it. If it is gone, the provider starts a new one from its latest snapshot. Two callers that connect to the same ID share one sandbox. Their turns run one at a time.
- `hatchet.sandbox.list({ metadata })` finds sandboxes by their tags. Each result says if the sandbox is live and names its latest snapshot.
- `options.from` starts the sandbox from a snapshot. Two sandboxes with the same `from` are a fork.

### Use the sandbox

- `sandbox.exec(cmd)` runs a shell command. It returns `{ stdout, stderr, exitCode }`. A non-zero exit code does not cause an error.
- `sandbox.kill()` removes the sandbox. The provider also removes a sandbox after 15 minutes without a turn. Set `--sandbox-idle-ttl` on the provider, or `idleTimeoutMs` on `create`, to change this time.

Each call is one Hatchet task. Each `exec` result also has `snapshotId` and `timings`.

### Drivers

The provider has two drivers. Set `--driver` or `BRICKIE_DRIVER`.

- `docker` (default): a plain container on any Docker engine. Good for development and CI. It is not a VM. Do not run untrusted agents in it.
- `sbx`: a Docker Sandboxes micro-VM. macOS only. Use it for untrusted agents on a laptop. Start the demo with `hack/run-demo.sh --driver sbx`.

No host environment variables reach a sandbox on either driver.

### Use files

`sandbox.files` has the file operations. The shape follows the E2B filesystem API. Paths are relative to the workspace. Content is exact, with no trimming.

- `read(path, { format })` returns text. With `format: 'bytes'` or `'blob'`, it returns binary data.
- `write(path, data)` and `writeFiles([{ path, data }])` write files and make the parent directories.
- `list(path, { depth })`, `exists(path)`, and `getInfo(path)` return file information.
- `makeDir(path)`, `rename(oldPath, newPath)`, `copy(srcPath, dstPath)`, and `remove(path)` change the workspace.
- `download(url, path)` and `upload(path, url)` move a file between the sandbox and a URL. Use them for files larger than 2 MiB.

A file error throws `FilesystemError` with a `code`, for example `not_found`. Calls that only read make no snapshot.

Run `hack/run-demo.sh fs` to see each operation. The code is in `examples/typescript/fs-demo.ts`.

### API reference

Run `pnpm docs` in `sdks/typescript` and open `sdks/typescript/docs/index.html`. TypeDoc makes the reference from the source.

### Connect to Hatchet

Set `HATCHET_CLIENT_TOKEN` to use a Hatchet instance. The provider then runs the turns.

Without `HATCHET_CLIENT_TOKEN`, the example starts an embedded Hatchet engine. It also starts the provider. This needs the `brickie` binary. Put it on `PATH`, or set `BRICKIE_BIN` to its location.

## Use an external Hatchet instance

The Hatchet in Docker Compose is the default. To use Hatchet Cloud or a self-hosted Hatchet instance:

1. Get a token from the tenant.
2. Put the token in `.env` as `HATCHET_CLIENT_TOKEN`. Replace the token from the script.
3. Remove the line `HATCHET_CLIENT_TLS_STRATEGY=none`. This line is only for the local Hatchet.
4. Run `hack/run-demo.sh` again.

The script finds the external token. It does not start the local Hatchet. It restarts the provider with that instance.
