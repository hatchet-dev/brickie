import assert from 'node:assert';
import { initClient } from './client.js';
import { e2b, sandbox } from '@hatchet/sandbox';

async function main() {
  const hatchet = await initClient();

  const sbx = sandbox(hatchet, { backend: e2b(), turnTimeout: '15m' });

  const session = hatchet.durableTask({
    name: 'e2b-session',
    executionTimeout: '30m',
    fn: async (_input: {}, ctx) => {
      const t1 = await sbx.executeCommand(ctx, 'echo one > log.txt && uname -a');
      const t2 = await sbx.executeCommand(ctx, 'echo two >> log.txt && wc -l < log.txt', {
        from: t1.snapshotId,
      });
      const [a, b] = await sbx.executeAll(ctx, [
        { command: "echo a >> log.txt && tr '\\n' ' ' < log.txt", from: t2.snapshotId },
        { command: "echo b >> log.txt && tr '\\n' ' ' < log.txt", from: t2.snapshotId },
      ]);
      return { t1, t2, a, b };
    },
  });

  const worker = await hatchet.worker('e2b-example-worker', { workflows: [session] });
  void worker.start();

  const res = await session.run({});
  console.log('session result:', JSON.stringify(res, null, 2));

  assert.equal((res.t2.output as any).stdout, '2');
  assert.equal((res.a.output as any).stdout.trim(), 'one two a');
  assert.equal((res.b.output as any).stdout.trim(), 'one two b');
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
