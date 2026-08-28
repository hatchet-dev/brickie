import { execFile } from 'node:child_process';
import * as fs from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';
import { promisify } from 'node:util';
import { JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import { SandboxDriver, TurnRequest, TurnResponse } from './provider.js';

const exec = promisify(execFile);

export type LocalTurnFn = (
  input: JsonObject,
  sbContext: { workspace: string }
) => JsonObject | Promise<JsonObject>;

export const RUNNER = `
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const child_process = require('node:child_process');
(async () => {
  const here = __dirname;
  const workspace = path.dirname(here);
  const { fnSource, input } = JSON.parse(fs.readFileSync(path.join(here, 'input.json'), 'utf8'));
  const fn = eval('(' + fnSource + ')');
  const output = await fn(input, { workspace });
  fs.writeFileSync(path.join(here, 'output.json'), JSON.stringify(output ?? {}));
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`;

async function sbx(args: string[], timeoutMs = 600_000): Promise<string> {
  try {
    const { stdout } = await exec('sbx', args, { timeout: timeoutMs, maxBuffer: 64 * 1024 * 1024 });
    return stdout;
  } catch (err: any) {
    const detail = `${err.stderr ?? ''}${err.stdout ?? ''}`;
    if (/not authenticated|sbx login/i.test(detail)) {
      throw new Error('Docker Sandboxes is not authenticated: run `sbx login` and retry');
    }
    throw new Error(`sbx ${args[0]} failed: ${detail || err.message}`);
  }
}

export class LocalDriver implements SandboxDriver {
  constructor(
    private fns: Record<string, LocalTurnFn>,
    private agent = 'claude'
  ) {}

  async runTurn(req: TurnRequest): Promise<TurnResponse> {
    const fn = this.fns[req.fn];
    if (!fn) {
      throw new Error(`unknown sandbox fn: ${req.fn}`);
    }
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), `${req.sandboxName}-`));
    const workspace = path.join(dir, 'workspace');
    const ctrl = path.join(workspace, '.brickie');
    await fs.mkdir(ctrl, { recursive: true });
    try {
      if (req.snapshotTar) {
        const inTar = path.join(dir, 'in.tar.gz');
        await fs.writeFile(inTar, req.snapshotTar);
        await exec('tar', ['-xzf', inTar, '-C', workspace]);
      }
      await fs.writeFile(path.join(ctrl, 'runner.cjs'), RUNNER);
      await fs.writeFile(
        path.join(ctrl, 'input.json'),
        JSON.stringify({ fnSource: fn.toString(), input: req.input })
      );
      await sbx([
        'create',
        this.agent,
        '--name',
        req.sandboxName,
        '-q',
        ...(req.image ? ['-t', req.image] : []),
        workspace,
      ]);
      await sbx(['exec', '-w', workspace, req.sandboxName, 'node', '.brickie/runner.cjs']);
      const output = JSON.parse(await fs.readFile(path.join(ctrl, 'output.json'), 'utf8'));
      await fs.rm(ctrl, { recursive: true, force: true });
      const outTar = path.join(dir, 'out.tar.gz');
      await exec('tar', ['-czf', outTar, '-C', workspace, '.']);
      const snapshotTar = await fs.readFile(outTar);
      return { output, snapshotTar };
    } finally {
      await sbx(['rm', '-f', req.sandboxName]).catch(() => {});
      await fs.rm(dir, { recursive: true, force: true });
    }
  }
}
