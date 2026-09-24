import { spawn } from 'node:child_process';
import { DurableContext, HatchetClient, JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import { setupTelemetry } from './otel.js';

export const SANDBOX_TASK_NAME = 'brickie-sandbox';

const COMMAND_FN = '$command';
const PROMPT_FN = '$prompt';
const DESTROY_FN = '$destroy';

export interface SandboxTurnInput extends JsonObject {
  sandboxId: string;
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
  from?: string;
}

export interface ExecuteOpts {
  key?: string;
}

export type ExecuteItem =
  | { command: string }
  | { prompt: string }
  | { fn: string; fnSource: string; input?: JsonObject };

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
const sandboxSeq = new WeakMap<object, number>();

function nextKey(ctx: object) {
  const n = turnSeq.get(ctx) ?? 0;
  turnSeq.set(ctx, n + 1);
  return `sbx-${n}`;
}

// Sandbox ids derive from the run id and creation order, so a replayed run
// rebuilds the same ids and its turns land in the same live sandboxes.
function nextSandboxId(ctx: Ctx) {
  const n = sandboxSeq.get(ctx) ?? 0;
  sandboxSeq.set(ctx, n + 1);
  return `${ctx.workflowRunId()}-${n}`;
}

export function sandbox(hatchet: HatchetClient, opts: SandboxOpts = {}) {
  void setupTelemetry();
  ensureLocalProvider(hatchet);

  let id: string | undefined;
  const sandboxId = (ctx: Ctx) => (id ??= nextSandboxId(ctx));

  const turn = (ctx: Ctx, fn: string, input: JsonObject, fnSource?: string): SandboxTurnInput => ({
    sandboxId: sandboxId(ctx),
    ...(opts.image ? { image: opts.image } : {}),
    fn,
    ...(fnSource ? { fnSource } : {}),
    input,
    ...(opts.from ? { snapshotId: opts.from } : {}),
  });

  const turnMetadata = (turnInput: SandboxTurnInput): Record<string, string> => ({
    'brickie.sandbox': turnInput.sandboxId,
    'brickie.fn': turnInput.fn,
    ...(turnInput.image ? { 'brickie.image': turnInput.image } : {}),
    ...(turnInput.snapshotId ? { 'brickie.from': turnInput.snapshotId } : {}),
  });

  const spawnTurn = (ctx: Ctx, turnInput: SandboxTurnInput, key?: string): Promise<TurnResult> =>
    ctx.spawnChild<SandboxTurnInput, TurnResult>(SANDBOX_TASK_NAME, turnInput, {
      key: key ?? nextKey(ctx),
      additionalMetadata: turnMetadata(turnInput),
    });

  const itemToTurn = (ctx: Ctx, item: ExecuteItem): SandboxTurnInput => {
    if ('command' in item) {
      return turn(ctx, COMMAND_FN, { cmd: item.command });
    }
    if ('prompt' in item) {
      return turn(ctx, PROMPT_FN, { prompt: item.prompt });
    }
    return turn(ctx, item.fn, item.input ?? {}, item.fnSource);
  };

  return {
    createFunction(name: string, fn: SandboxFn) {
      const fnSource = fn.toString();
      return {
        name,
        fnSource,
        run: (ctx: Ctx, input: JsonObject = {}, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
          spawnTurn(ctx, turn(ctx, name, input, fnSource), execOpts.key),
      };
    },
    executeCommand: (ctx: Ctx, command: string, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
      spawnTurn(ctx, turn(ctx, COMMAND_FN, { cmd: command }), execOpts.key),
    executePrompt: (ctx: Ctx, prompt: string, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
      spawnTurn(ctx, turn(ctx, PROMPT_FN, { prompt }), execOpts.key),
    executeAll: (ctx: Ctx, items: ExecuteItem[]): Promise<TurnResult[]> =>
      ctx.spawnChildren<SandboxTurnInput, TurnResult>(
        items.map((item) => {
          const turnInput = itemToTurn(ctx, item);
          return {
            workflow: SANDBOX_TASK_NAME,
            input: turnInput,
            options: { key: nextKey(ctx), additionalMetadata: turnMetadata(turnInput) },
          };
        })
      ),
    destroy: (ctx: Ctx, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
      spawnTurn(ctx, turn(ctx, DESTROY_FN, {}), execOpts.key),
  };
}
