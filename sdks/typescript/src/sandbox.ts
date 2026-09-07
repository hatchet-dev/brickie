import { spawn } from 'node:child_process';
import { Duration, DurableContext, HatchetClient, JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import type { SandboxBackend } from './backend.js';

export const SANDBOX_TASK_NAME = 'brickie-sandbox';

const COMMAND_FN = '$command';
const PROMPT_FN = '$prompt';

export interface SandboxTurnInput extends JsonObject {
  image?: string;
  fn: string;
  fnSource?: string;
  input: JsonObject;
  snapshotId?: string;
}

export interface TurnResult extends JsonObject {
  output: JsonObject;
  snapshotId: string;
}

export type SandboxFn = (
  input: JsonObject,
  sbContext: { workspace: string }
) => JsonObject | Promise<JsonObject>;

export interface SandboxOpts {
  image?: string;
  backend?: SandboxBackend;
  credentials?: string[];
  turnTimeout?: Duration;
}

export interface ExecuteOpts {
  from?: string;
  key?: string;
}

export type ExecuteItem = { from?: string } & (
  | { command: string }
  | { prompt: string }
  | { fn: string; fnSource: string; input?: JsonObject }
);

type Ctx = DurableContext<any>;

let sidecarStarted = false;

function ensureLocalProvider(hatchet: HatchetClient) {
  if (sidecarStarted || process.env.HATCHET_CLIENT_TOKEN) {
    return;
  }
  sidecarStarted = true;
  const bin = process.env.BRICKIE_BIN ?? 'brickie';
  const cfg = hatchet.config;
  const child = spawn(bin, ['worker', '--driver', 'local'], {
    stdio: ['ignore', 'inherit', 'inherit'],
    env: {
      ...process.env,
      HATCHET_CLIENT_TOKEN: cfg.token,
      HATCHET_CLIENT_HOST_PORT: cfg.host_port,
      HATCHET_CLIENT_TLS_STRATEGY: cfg.tls_config?.tls_strategy ?? 'none',
    },
  });
  child.on('error', (err) => {
    console.error(
      `brickie: failed to start local sandbox provider (${bin}): ${err.message}\n` +
        'install it with: go install github.com/hatchet-dev/brickie/cmd/brickie@latest (or set BRICKIE_BIN)'
    );
  });
  process.on('exit', () => child.kill());
}

const backendProvidersStarted = new Set<string>();

function ensureBackendProvider(
  hatchet: HatchetClient,
  backend: SandboxBackend,
  credentials: string[],
  turnTimeout?: Duration
) {
  if (backendProvidersStarted.has(backend.name)) {
    return;
  }
  backendProvidersStarted.add(backend.name);
  void (async () => {
    const { runTurnOnBackend } = await import('./backend.js');
    const task = hatchet.durableTask<SandboxTurnInput, TurnResult>({
      name: SANDBOX_TASK_NAME,
      executionTimeout: turnTimeout ?? '15m',
      fn: (turn) => runTurnOnBackend(backend, turn, credentials),
    });
    const worker = await hatchet.worker(`brickie-${backend.name}-provider`, { workflows: [task] });
    void worker.start();
  })();
}

export function sandbox(hatchet: HatchetClient, opts: SandboxOpts = {}) {
  if (opts.backend) {
    ensureBackendProvider(hatchet, opts.backend, opts.credentials ?? [], opts.turnTimeout);
  } else {
    ensureLocalProvider(hatchet);
  }

  const seq = new WeakMap<object, number>();
  const nextKey = (ctx: object) => {
    const n = seq.get(ctx) ?? 0;
    seq.set(ctx, n + 1);
    return `sbx-${n}`;
  };

  const turn = (fn: string, input: JsonObject, from?: string, fnSource?: string): SandboxTurnInput => ({
    ...(opts.image ? { image: opts.image } : {}),
    fn,
    ...(fnSource ? { fnSource } : {}),
    input,
    ...(from ? { snapshotId: from } : {}),
  });

  const spawnTurn = (ctx: Ctx, turnInput: SandboxTurnInput, key?: string): Promise<TurnResult> =>
    ctx.spawnChild<SandboxTurnInput, TurnResult>(SANDBOX_TASK_NAME, turnInput, {
      key: key ?? nextKey(ctx),
    });

  const itemToTurn = (item: ExecuteItem): SandboxTurnInput => {
    if ('command' in item) {
      return turn(COMMAND_FN, { cmd: item.command }, item.from);
    }
    if ('prompt' in item) {
      return turn(PROMPT_FN, { prompt: item.prompt }, item.from);
    }
    return turn(item.fn, item.input ?? {}, item.from, item.fnSource);
  };

  return {
    createFunction(name: string, fn: SandboxFn) {
      const fnSource = fn.toString();
      return {
        name,
        fnSource,
        run: (ctx: Ctx, input: JsonObject = {}, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
          spawnTurn(ctx, turn(name, input, execOpts.from, fnSource), execOpts.key),
      };
    },
    executeCommand: (ctx: Ctx, command: string, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
      spawnTurn(ctx, turn(COMMAND_FN, { cmd: command }, execOpts.from), execOpts.key),
    executePrompt: (ctx: Ctx, prompt: string, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
      spawnTurn(ctx, turn(PROMPT_FN, { prompt }, execOpts.from), execOpts.key),
    executeAll: (ctx: Ctx, items: ExecuteItem[]): Promise<TurnResult[]> =>
      ctx.spawnChildren<SandboxTurnInput, TurnResult>(
        items.map((item) => ({
          workflow: SANDBOX_TASK_NAME,
          input: itemToTurn(item),
          options: { key: nextKey(ctx) },
        }))
      ),
  };
}
