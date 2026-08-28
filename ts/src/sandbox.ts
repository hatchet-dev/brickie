import {
  Duration,
  DurableContext,
  HatchetClient,
  JsonObject,
} from '@hatchet-dev/typescript-sdk/v1';
import { COMMAND_FN, PROMPT_FN, builtinFns } from './builtins.js';
import { LocalDriver, LocalTurnFn } from './local-driver.js';
import { sandboxProvider } from './provider.js';

export const SANDBOX_TASK_NAME = 'brickie-sandbox';

export interface SandboxTurnInput extends JsonObject {
  image?: string;
  fn: string;
  input: JsonObject;
  snapshotId?: string;
}

export interface TurnResult extends JsonObject {
  output: JsonObject;
  snapshotId: string;
}

export interface SandboxOpts {
  image?: string;
  turnTimeout?: Duration;
}

export interface ExecuteOpts {
  from?: string;
  key?: string;
}

export interface SandboxFunction {
  readonly fnName: string;
  run(ctx: DurableContext<any>, input?: JsonObject, execOpts?: ExecuteOpts): Promise<TurnResult>;
}

export type ExecuteItem = { from?: string } & (
  | { command: string }
  | { prompt: string }
  | { fn: SandboxFunction; input?: JsonObject }
);

type Ctx = DurableContext<any>;

const localFns: Record<string, LocalTurnFn> = builtinFns();
let localProviderStarted = false;

function ensureProvider(hatchet: HatchetClient, turnTimeout?: Duration) {
  if (localProviderStarted) {
    return;
  }
  localProviderStarted = true;
  void (async () => {
    const task = sandboxProvider(hatchet, { driver: new LocalDriver(localFns), turnTimeout });
    const worker = await hatchet.worker('brickie-local-provider', { workflows: [task] });
    void worker.start();
  })();
}

export function sandbox(hatchet: HatchetClient, opts: SandboxOpts = {}) {
  if (!process.env.HATCHET_CLIENT_TOKEN) {
    ensureProvider(hatchet, opts.turnTimeout);
  }

  const seq = new WeakMap<object, number>();
  const nextKey = (ctx: object) => {
    const n = seq.get(ctx) ?? 0;
    seq.set(ctx, n + 1);
    return `sbx-${n}`;
  };

  const turn = (fn: string, input: JsonObject, from?: string): SandboxTurnInput => ({
    ...(opts.image ? { image: opts.image } : {}),
    fn,
    input,
    ...(from ? { snapshotId: from } : {}),
  });

  const spawn = (ctx: Ctx, turnInput: SandboxTurnInput, key?: string): Promise<TurnResult> =>
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
    return turn(item.fn.fnName, item.input ?? {}, item.from);
  };

  return {
    createFunction(name: string, fn: LocalTurnFn): SandboxFunction {
      localFns[name] = fn;
      return {
        fnName: name,
        run: (ctx: Ctx, input: JsonObject = {}, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
          spawn(ctx, turn(name, input, execOpts.from), execOpts.key),
      };
    },
    executeCommand: (ctx: Ctx, command: string, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
      spawn(ctx, turn(COMMAND_FN, { cmd: command }, execOpts.from), execOpts.key),
    executePrompt: (ctx: Ctx, prompt: string, execOpts: ExecuteOpts = {}): Promise<TurnResult> =>
      spawn(ctx, turn(PROMPT_FN, { prompt }, execOpts.from), execOpts.key),
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
