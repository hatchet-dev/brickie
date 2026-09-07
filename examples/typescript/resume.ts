import assert from 'node:assert';
import { initClient } from './client.js';
import { sandbox } from '@hatchet/sandbox';

async function main() {
  const snapshotId = process.env.SNAPSHOT_ID;
  if (!snapshotId) {
    console.error('usage: SNAPSHOT_ID=<id from a previous session> pnpm resume');
    process.exit(2);
  }
  const hatchet = await initClient();
  const sbx = sandbox(hatchet);

  const session = hatchet.durableTask({
    name: 'resume-session',
    executionTimeout: '30m',
    fn: async (input: { from: string }, ctx) => {
      const check = await sbx.executeCommand(ctx, 'ls && echo --- && cat log.txt', {
        from: input.from,
      });
      return { check };
    },
  });

  const worker = await hatchet.worker('resume-worker', { workflows: [session] });
  void worker.start();

  const res = await session.run({ from: snapshotId });
  console.log('restored workspace:', JSON.stringify(res.check.output, null, 2));
  assert.equal((res.check.output as any).exitCode, 0);
  console.log('restore ok');

  await worker.stop();
  await hatchet.stopEmbedded?.();
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
