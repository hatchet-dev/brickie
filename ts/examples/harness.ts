import assert from 'node:assert';
import * as child_process from 'node:child_process';
import * as fs from 'node:fs';
import { initClient } from './client.js';
import { sandbox } from '../src/index.js';

async function main() {
  const hatchet = await initClient();

  const sbx = sandbox(hatchet, { turnTimeout: '15m' });

  const agentTurn = sbx.createFunction('agentTurn', async (input, sbContext) => {
    if (!fs.existsSync(`${sbContext.workspace}/node_modules/@anthropic-ai/claude-agent-sdk`)) {
      child_process.execSync(
        'npm init -y && npm install --no-audit --no-fund @anthropic-ai/claude-agent-sdk',
        { cwd: sbContext.workspace, stdio: 'ignore' }
      );
    }
    const sdkName = '@anthropic-ai/claude-agent-sdk';
    const { query } = (await import(sdkName)) as any;
    const transcript: string[] = [];
    let reply = '';
    for await (const msg of query({
      prompt: `Your workspace is ${sbContext.workspace} — create and modify files only there. ${String(input.prompt)}`,
      options: {
        cwd: sbContext.workspace,
        permissionMode: 'bypassPermissions',
        allowedTools: ['Read', 'Write', 'Edit', 'Bash'],
      },
    })) {
      if (msg.type === 'assistant') {
        for (const block of msg.message?.content ?? []) {
          if (block.type === 'tool_use') {
            transcript.push(`tool:${block.name}`);
          }
        }
      }
      if (msg.type === 'result') {
        reply = msg.subtype === 'success' ? msg.result : msg.subtype;
      }
    }
    const files = fs
      .readdirSync(sbContext.workspace)
      .filter((f) => !['.brickie', 'node_modules', 'package.json', 'package-lock.json'].includes(f));
    return { reply, files, transcript };
  });

  const session = hatchet.durableTask({
    name: 'harness-session',
    executionTimeout: '1h',
    fn: async (input: { goal: string }, ctx) => {
      const t1Prompt = `${input.goal}. Write the result to result.md. Do not ask questions. Reply with exactly: done`;
      let t1 = await agentTurn.run(ctx, { prompt: t1Prompt });
      for (let attempt = 0; !(t1.output.files as string[]).includes('result.md') && attempt < 2; attempt++) {
        t1 = await agentTurn.run(ctx, {
          prompt: `${t1Prompt} You have not created the file yet: you MUST call the Write tool before replying.`,
        });
      }
      if (!(t1.output.files as string[]).includes('result.md')) {
        throw new Error(`turn 1 never created result.md; last reply: ${t1.output.reply}`);
      }
      const t2 = await agentTurn.run(
        ctx,
        { prompt: 'Review result.md and append a one-line summary at the bottom. Reply with exactly: done' },
        { from: t1.snapshotId }
      );
      const check = await sbx.executeCommand(ctx, 'cat result.md', { from: t2.snapshotId });
      return { t1, t2, check };
    },
  });

  const worker = await hatchet.worker('harness-worker', { workflows: [session] });
  void worker.start();

  const res = await session.run({ goal: 'Write a short haiku about durable execution' });
  console.log('session result:', JSON.stringify(res, null, 2));

  assert.ok((res.t1.output as any).files.includes('result.md'));
  assert.ok((res.t1.output as any).transcript.length > 0);
  assert.equal((res.check.output as any).exitCode, 0);
  assert.ok((res.check.output as any).stdout.length > 0);
  console.log('e2e ok');

  await worker.stop();
  await hatchet.stopEmbedded?.();
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
