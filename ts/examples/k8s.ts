import assert from 'node:assert';
import { initClient } from './client.js';
import { sandbox } from '../src/index.js';

async function main() {
  const hatchet = await initClient();

  const sbx = sandbox(hatchet, { turnTimeout: '15m' });

  const session = hatchet.durableTask({
    name: 'k8s-session',
    executionTimeout: '30m',
    fn: async (_input: {}, ctx) => {
      const t1 = await sbx.executeCommand(ctx, 'echo one > log.txt && cat /etc/alpine-release');
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

  const worker = await hatchet.worker('k8s-worker', { workflows: [session] });
  void worker.start();

  const res = await session.run({});
  console.log('session result:', JSON.stringify(res, null, 2));

  assert.match((res.t1.output as any).stdout, /^\d+\./);
  assert.equal((res.t2.output as any).stdout, '2');
  assert.equal((res.a.output as any).stdout, 'one two a');
  assert.equal((res.b.output as any).stdout, 'one two b');
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
