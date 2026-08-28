import * as child_process from 'node:child_process';
import * as fs from 'node:fs';
import { LocalTurnFn } from './local-driver.js';

export const COMMAND_FN = '$command';
export const PROMPT_FN = '$prompt';

export function builtinFns(): Record<string, LocalTurnFn> {
  return { [COMMAND_FN]: commandFn, [PROMPT_FN]: promptFn };
}

export const commandFn: LocalTurnFn = async (input, sbContext) => {
  const res = child_process.spawnSync('sh', ['-c', String(input.cmd)], {
    cwd: sbContext.workspace,
    encoding: 'utf8',
  });
  return {
    stdout: (res.stdout ?? '').trim(),
    stderr: (res.stderr ?? '').trim(),
    exitCode: res.status ?? -1,
  };
};

export const promptFn: LocalTurnFn = async (input, sbContext) => {
  const res = child_process.spawnSync(
    'claude',
    [
      '-p',
      `Your workspace is ${sbContext.workspace} — create and modify files only there. ${String(input.prompt)}`,
      '--dangerously-skip-permissions',
      '--output-format',
      'json',
    ],
    { cwd: sbContext.workspace, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 }
  );
  if (res.status !== 0) {
    throw new Error(`claude failed (exit ${res.status}): ${res.stderr || res.stdout}`);
  }
  let reply = res.stdout.trim();
  try {
    const parsed = JSON.parse(reply);
    reply = parsed.result ?? parsed.subtype ?? reply;
  } catch {}
  const files = fs
    .readdirSync(sbContext.workspace)
    .filter((f) => !['.brickie', 'node_modules', 'package.json', 'package-lock.json'].includes(f));
  return { reply, files };
};
