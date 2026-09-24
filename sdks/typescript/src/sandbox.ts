import { spawn } from 'node:child_process';
import { DurableContext, HatchetClient, JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import { setupTelemetry } from './otel.js';

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
  timings?: Record<string, number>;
}

export type SandboxFn = (
  input: JsonObject,
  sbContext: { workspace: string }
) => JsonObject | Promise<JsonObject>;

export interface SandboxOpts {
  image?: string;
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
  const child = spawn(bin, ['worker'], {
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

export type Sandbox = ReturnType<typeof sandbox>;

declare module '@hatchet-dev/typescript-sdk/v1' {
  interface HatchetClient {
    sandbox(opts?: SandboxOpts): Sandbox;
  }
}

HatchetClient.prototype.sandbox = function (this: HatchetClient, opts: SandboxOpts = {}) {
  return sandbox(this, opts);
};

const turnSeq = new WeakMap<object, number>();

function nextKey(ctx: object) {
  const n = turnSeq.get(ctx) ?? 0;
  turnSeq.set(ctx, n + 1);
  return `sbx-${n}`;
}

export function sandbox(hatchet: HatchetClient, opts: SandboxOpts = {}) {
  void setupTelemetry();
  ensureLocalProvider(hatchet);

  const turn = (fn: string, input: JsonObject, from?: string, fnSource?: string): SandboxTurnInput => ({
    ...(opts.image ? { image: opts.image } : {}),
    fn,
    ...(fnSource ? { fnSource } : {}),
    input,
    ...(from ? { snapshotId: from } : {}),
  });

  const turnMetadata = (turnInput: SandboxTurnInput): Record<string, string> => ({
    'brickie.fn': turnInput.fn,
    ...(turnInput.image ? { 'brickie.image': turnInput.image } : {}),
    ...(turnInput.snapshotId ? { 'brickie.from': turnInput.snapshotId } : {}),
  });

  const spawnTurn = (ctx: Ctx, turnInput: SandboxTurnInput, key?: string): Promise<TurnResult> =>
    ctx.spawnChild<SandboxTurnInput, TurnResult>(SANDBOX_TASK_NAME, turnInput, {
      key: key ?? nextKey(ctx),
      additionalMetadata: turnMetadata(turnInput),
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
        items.map((item) => {
          const turnInput = itemToTurn(item);
          return {
            workflow: SANDBOX_TASK_NAME,
            input: turnInput,
            options: { key: nextKey(ctx), additionalMetadata: turnMetadata(turnInput) },
          };
        })
      ),
  };
}
