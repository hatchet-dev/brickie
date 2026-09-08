import { JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import { SandboxTurnInput, TurnResult } from './sandbox.js';

export interface ExecResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export interface BackendSession {
  exec(command: string): Promise<ExecResult>;
  checkpoint(): Promise<string>;
  destroy(): Promise<void>;
}

export interface SandboxBackend {
  name: string;
  createSession(opts: {
    image?: string;
    snapshotId?: string;
    env?: Record<string, string>;
  }): Promise<BackendSession>;
}

const RUNNER_JS = `
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

const EXCLUDED_FILES = ['.brickie', 'node_modules', 'package.json', 'package-lock.json'];

function sq(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

function inWorkspace(workspace: string, command: string): string {
  return `cd ${sq(workspace)} && { ${command}\n}`;
}

async function mustExec(session: BackendSession, command: string): Promise<ExecResult> {
  const res = await session.exec(command);
  if (res.exitCode !== 0) {
    throw new Error(`backend exec failed (exit ${res.exitCode}): ${res.stderr || res.stdout}`);
  }
  return res;
}

async function writeFileB64(session: BackendSession, workspace: string, path: string, content: string): Promise<void> {
  const b64 = Buffer.from(content, 'utf8').toString('base64');
  await mustExec(session, inWorkspace(workspace, `mkdir -p "$(dirname ${sq(path)})" && printf %s ${sq(b64)} | base64 -d > ${sq(path)}`));
}

async function readFileB64(session: BackendSession, workspace: string, path: string): Promise<string> {
  const res = await mustExec(session, inWorkspace(workspace, `base64 < ${sq(path)}`));
  return Buffer.from(res.stdout.replace(/\s/g, ''), 'base64').toString('utf8');
}

export async function runTurnOnBackend(
  backend: SandboxBackend,
  turn: SandboxTurnInput,
  credentials: string[] = [],
  log: (message: string) => void = () => {}
): Promise<TurnResult> {
  const env: Record<string, string> = {};
  for (const name of credentials) {
    const value = process.env[name];
    if (value !== undefined) {
      env[name] = value;
    }
  }
  log(`turn start: backend=${backend.name} fn=${turn.fn} image=${turn.image ?? 'default'} snapshot=${turn.snapshotId ?? 'none'}`);
  const createStart = Date.now();
  const session = await backend.createSession({
    image: turn.image as string | undefined,
    snapshotId: turn.snapshotId as string | undefined,
    env,
  });
  log(`backend session ready after ${Date.now() - createStart}ms`);
  try {
    const workspace = (await mustExec(session, 'pwd')).stdout.trim() || '/';
    const input = (turn.input ?? {}) as JsonObject;
    let output: JsonObject;

    if (turn.fn === '$command') {
      const res = await session.exec(inWorkspace(workspace, String(input.cmd)));
      output = { stdout: res.stdout.trim(), stderr: res.stderr.trim(), exitCode: res.exitCode };
    } else if (turn.fn === '$prompt') {
      const prompt = `Your workspace is ${workspace} — create and modify files only there. ${String(input.prompt)}`;
      const res = await session.exec(
        inWorkspace(workspace, `claude -p ${sq(prompt)} --dangerously-skip-permissions --output-format json`)
      );
      if (res.exitCode !== 0) {
        throw new Error(`claude failed (exit ${res.exitCode}): ${res.stderr || res.stdout}`);
      }
      let reply = res.stdout.trim();
      try {
        const parsed = JSON.parse(reply);
        reply = parsed.result ?? parsed.subtype ?? reply;
      } catch {}
      const ls = await mustExec(session, inWorkspace(workspace, 'ls -1'));
      const files = ls.stdout.split('\n').map((f) => f.trim()).filter((f) => f && !EXCLUDED_FILES.includes(f));
      output = { reply, files };
    } else {
      if (!turn.fnSource) {
        throw new Error(`unknown sandbox fn ${turn.fn} and no fnSource provided`);
      }
      await writeFileB64(session, workspace, '.brickie/runner.cjs', RUNNER_JS);
      await writeFileB64(
        session,
        workspace,
        '.brickie/input.json',
        JSON.stringify({ fnSource: turn.fnSource, input })
      );
      const run = await session.exec(inWorkspace(workspace, 'node .brickie/runner.cjs'));
      if (run.exitCode !== 0) {
        throw new Error(`sandbox fn failed (exit ${run.exitCode}): ${run.stderr || run.stdout}`);
      }
      output = JSON.parse(await readFileB64(session, workspace, '.brickie/output.json'));
      await session.exec(inWorkspace(workspace, 'rm -rf .brickie'));
    }

    log(`fn ${turn.fn} executed`);
    const snapshotId = await session.checkpoint();
    log(`snapshot ${snapshotId} stored`);
    return { output, snapshotId };
  } finally {
    await session.destroy().catch(() => {});
    log('backend session destroyed');
  }
}
