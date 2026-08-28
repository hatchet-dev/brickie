import assert from 'node:assert';
import { initClient } from './client.js';
import { sandbox } from '../src/index.js';

async function main() {
  const hatchet = await initClient();

  const sbx = sandbox(hatchet, { turnTimeout: '15m' });

  const session = hatchet.durableTask({
    name: 'agent-session',
    executionTimeout: '1h',
    fn: async (_input: { goal: string }, ctx) => {
      const t1Prompt =
        'Use the Write tool to create a file named haiku.txt in the current directory containing a haiku about ephemeral sandboxes. Do not ask questions. Reply with exactly: done';
      let t1 = await sbx.executePrompt(ctx, t1Prompt);
      for (let attempt = 0; !(t1.output.files as string[]).includes('haiku.txt') && attempt < 2; attempt++) {
        t1 = await sbx.executePrompt(ctx, `${t1Prompt} You have not created the file yet: you MUST call the Write tool before replying.`);
      }
      if (!(t1.output.files as string[]).includes('haiku.txt')) {
        throw new Error(`turn 1 never created haiku.txt; last reply: ${t1.output.reply}`);
      }
      const t2 = await sbx.executePrompt(
        ctx,
        'Append a second stanza to haiku.txt. Reply with exactly: done',
        { from: t1.snapshotId }
      );
      const [a, b] = await sbx.executeAll(ctx, [
        { prompt: 'Copy haiku.txt to variant-a.txt. Reply with exactly: done', from: t2.snapshotId },
        { prompt: 'Copy haiku.txt to variant-b.txt. Reply with exactly: done', from: t2.snapshotId },
      ]);
      const check = await sbx.executeCommand(ctx, 'ls && echo --- && cat haiku.txt', {
        from: a.snapshotId,
      });
      return { t1, t2, a, b, check };
    },
  });

  const worker = await hatchet.worker('my-worker', { workflows: [session] });
  void worker.start();

  const res = await session.run({ goal: 'write haikus' });
  console.log('session result:', JSON.stringify(res, null, 2));

  assert.ok((res.t1.output as any).files.includes('haiku.txt'));
  assert.ok((res.a.output as any).files.includes('variant-a.txt'));
  assert.ok((res.b.output as any).files.includes('variant-b.txt'));
  assert.ok(!(res.a.output as any).files.includes('variant-b.txt'));
  assert.ok((res.check.output as any).stdout.includes('variant-a.txt'));
  assert.notEqual(res.a.snapshotId, res.b.snapshotId);
  console.log('e2e ok');

  await worker.stop();
  await hatchet.stopEmbedded?.();
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
