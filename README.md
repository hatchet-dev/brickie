# brickie

Sandboxes for agents, orchestrated by Hatchet. Every sandbox turn is a durable Hatchet task; every turn ends in a coordinated snapshot you can resume or fork from.

Requires macOS with Docker Desktop, Homebrew, and Node + pnpm.

## Track 1 — fully local (embedded Hatchet + Docker Sandboxes)

```bash
./scripts/setup-local.sh        # installs sbx CLI, Docker sign-in, network policy, Claude auth (opens browser), pnpm install
pnpm --dir ts example:command   # deterministic smoke test, no LLM
pnpm --dir ts example:prompt    # Claude Agent SDK across snapshot-chained turns + parallel forks
pnpm --dir ts example:harness   # full agent harness as a user-defined sandbox function
```

Success looks like `session result: {...}` then `e2e ok`. The snapshot IDs in the output are the coordinated snapshots — `resume` and forks chain off them. The first turn is slow (sandbox image pull + in-sandbox npm install of the Claude Agent SDK); later turns reuse it via the snapshot.

## Track 2 — Hatchet Cloud orchestrating, provider in kind

```bash
export HATCHET_CLIENT_TOKEN=<token from your cloud tenant>
export ANTHROPIC_API_KEY=<key>        # or CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`
./scripts/kind-deploy.sh brickie      # kind cluster + agent-sandbox + helm-installs the provider worker
pnpm --dir ts example:k8s
```

The client makes zero provider or driver decisions: with `HATCHET_CLIENT_TOKEN` set it only triggers durable tasks. The worker deployed in kind (visible in the dashboard as `hatchet-sandbox-provider`) claims them, runs each turn in an ephemeral `agents.x-k8s.io` Sandbox pod, snapshots the workspace, and tears the pod down. Without a token, examples run against an embedded Hatchet engine and a locally started provider.

> If you hack on hatchet locally with mkcert: `unset HATCHET_CLIENT_TLS_ROOT_CA_FILE` first, or gRPC to cloud fails with a TLS error.

## API

```ts
import { sandbox } from '@hatchet/sandbox';

const sbx = sandbox(hatchet, { turnTimeout: '15m' });

const session = hatchet.durableTask({
  name: 'agent-session',
  fn: async (input, ctx) => {
    const t1 = await sbx.executePrompt(ctx, 'create haiku.txt with a haiku');
    const t2 = await sbx.executeCommand(ctx, 'cat haiku.txt', { from: t1.snapshotId });
    const [a, b] = await sbx.executeAll(ctx, [
      { prompt: 'make it darker', from: t2.snapshotId },
      { prompt: 'make it funnier', from: t2.snapshotId },
    ]);
    return { a, b };
  },
});
```

- `executeCommand(ctx, cmd, opts?)` — shell command in the workspace
- `executePrompt(ctx, prompt, opts?)` — Claude Agent SDK harness turn
- `createFunction(name, fn)` — register a function that runs inside the sandbox; call via `.run(ctx, input, opts?)`
- `executeAll(ctx, items)` — parallel turns (one durable memo node)

All verbs are durable child tasks spawned from your orchestrator's `ctx`. `{ from: snapshotId }` resumes from a snapshot; the same `from` twice is a fork. Omit it for a fresh sandbox.

## Layout

- `ts/src/` — the `@hatchet/sandbox` SDK: user surface, provider (durable task), local sbx driver, Kubernetes driver, worker entrypoint
- `ts/examples/` — the four examples above
- `helm/hatchet-sandbox/` — deploys the provider worker (+ vendored kubernetes-sigs/agent-sandbox subchart); `HATCHET_CLIENT_TOKEN` required, credentials injected into sandboxes via `sandbox.credentials`
- `scripts/` — `setup-local.sh`, `kind-deploy.sh`
