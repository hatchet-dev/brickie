import { DurableContext, HatchetClient, JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import { setupTelemetry } from './otel.js';

const SANDBOX_TASK_NAME = 'brickie-sandbox';

type Ctx = DurableContext<any>;

interface TurnInput extends JsonObject {
  sandboxId: string;
  image?: string;
  fn: string;
  input: JsonObject;
  snapshotId?: string;
}

interface TurnResult extends JsonObject {
  output: JsonObject;
  snapshotId: string;
  timings?: Record<string, number>;
}

export interface Timings {
  snapshotId?: string;
  timings?: Record<string, number>;
}

export interface CommandResult extends Timings {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export interface PromptResult extends Timings {
  reply: string;
  files: string[];
}

export interface SandboxOpts {
  template?: string;
  from?: string;
}

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

export class Sandbox {
  readonly commands: { run: (cmd: string) => Promise<CommandResult> };

  private constructor(
    private readonly ctx: Ctx,
    readonly sandboxId: string,
    private readonly opts: SandboxOpts
  ) {
    this.commands = { run: (cmd) => this.turn<CommandResult>('$command', { cmd }) };
  }

  static async create(ctx: Ctx, opts: SandboxOpts = {}): Promise<Sandbox> {
    void setupTelemetry();
    const sandbox = new Sandbox(ctx, nextSandboxId(ctx), opts);
    await sandbox.turn('$create', {});
    return sandbox;
  }

  async prompt(text: string): Promise<PromptResult> {
    return this.turn<PromptResult>('$prompt', { prompt: text });
  }

  async kill(): Promise<void> {
    await this.turn('$destroy', {});
  }

  private async turn<T = JsonObject>(fn: string, input: JsonObject): Promise<T> {
    const turnInput: TurnInput = {
      sandboxId: this.sandboxId,
      ...(this.opts.template ? { image: this.opts.template } : {}),
      fn,
      input,
      ...(this.opts.from ? { snapshotId: this.opts.from } : {}),
    };
    const result = await this.ctx.spawnChild<TurnInput, TurnResult>(SANDBOX_TASK_NAME, turnInput, {
      key: nextKey(this.ctx),
      additionalMetadata: {
        'brickie.sandbox': this.sandboxId,
        'brickie.fn': fn,
        ...(turnInput.image ? { 'brickie.image': turnInput.image } : {}),
        ...(turnInput.snapshotId ? { 'brickie.from': turnInput.snapshotId } : {}),
      },
    });
    return { ...result.output, snapshotId: result.snapshotId, timings: result.timings } as unknown as T;
  }
}

export interface SandboxNamespace {
  create(ctx: Ctx, opts?: SandboxOpts): Promise<Sandbox>;
}

declare module '@hatchet-dev/typescript-sdk/v1' {
  interface HatchetClient {
    readonly sandbox: SandboxNamespace;
  }
}

Object.defineProperty(HatchetClient.prototype, 'sandbox', {
  configurable: true,
  get(): SandboxNamespace {
    return { create: Sandbox.create };
  },
});
