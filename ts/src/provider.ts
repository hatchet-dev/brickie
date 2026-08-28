import * as fs from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';
import { Duration, HatchetClient, JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import { SANDBOX_TASK_NAME, SandboxTurnInput, TurnResult } from './sandbox.js';

export interface TurnRequest {
  sandboxName: string;
  image?: string;
  fn: string;
  input: JsonObject;
  snapshotTar?: Buffer;
}

export interface TurnResponse {
  output: JsonObject;
  snapshotTar: Buffer;
}

export interface SandboxDriver {
  runTurn(req: TurnRequest): Promise<TurnResponse>;
}

export interface SandboxProviderOpts {
  driver: SandboxDriver;
  snapshotDir?: string;
  turnTimeout?: Duration;
}

export function sandboxProvider(hatchet: HatchetClient, opts: SandboxProviderOpts) {
  const snapshotDir = opts.snapshotDir ?? path.join(os.homedir(), '.brickie', 'snapshots');

  const readSnapshot = (id: string) => fs.readFile(path.join(snapshotDir, `${id}.tar.gz`));

  const writeSnapshot = async (id: string, tar: Buffer) => {
    await fs.mkdir(snapshotDir, { recursive: true });
    const dest = path.join(snapshotDir, `${id}.tar.gz`);
    const tmp = `${dest}.tmp-${process.pid}`;
    await fs.writeFile(tmp, tar);
    await fs.rename(tmp, dest);
  };

  return hatchet.durableTask<SandboxTurnInput, TurnResult>({
    name: SANDBOX_TASK_NAME,
    executionTimeout: opts.turnTimeout ?? '10m',
    fn: async (turn, ctx): Promise<TurnResult> => {
      const runId = ctx.workflowRunId();
      const res = await opts.driver.runTurn({
        sandboxName: `bx-${runId}`,
        image: turn.image,
        fn: turn.fn,
        input: turn.input,
        snapshotTar: turn.snapshotId ? await readSnapshot(turn.snapshotId) : undefined,
      });
      const snapshotId = runId;
      await writeSnapshot(snapshotId, res.snapshotTar);
      return { output: res.output, snapshotId };
    },
  });
}
