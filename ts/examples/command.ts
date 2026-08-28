import assert from 'node:assert';
import { initClient } from './client.js';
import { sandbox } from '../src/index.js';

async function main() {
  const hatchet = await initClient();

  const sbx = sandbox(hatchet, { turnTimeout: '15m' });

  const session = hatchet.durableTask({
    name: 'command-session',
    executionTimeout: '30m',
    fn: async (_input: {}, ctx) => {
      const t1 = await sbx.executeCommand(ctx, 'npm init -y >/dev/null && node --version');
      const t2 = await sbx.executeCommand(
        ctx,
        `node -e "const fs=require('fs');const p=JSON.parse(fs.readFileSync('package.json','utf8'));p.version='1.2.3';fs.writeFileSync('package.json',JSON.stringify(p));console.log(p.name)"`,
        { from: t1.snapshotId }
      );
      const [a, b] = await sbx.executeAll(ctx, [
        { command: `node -p "require('./package.json').version + '-a'"`, from: t2.snapshotId },
        { command: `node -p "require('./package.json').version + '-b'"`, from: t2.snapshotId },
      ]);
      return { t1, t2, a, b };
    },
  });

  const worker = await hatchet.worker('command-worker', { workflows: [session] });
  void worker.start();

  const res = await session.run({});
  console.log('session result:', JSON.stringify(res, null, 2));

  assert.match((res.t1.output as any).stdout, /^v\d+\./);
  assert.ok((res.t2.output as any).stdout.length > 0);
  assert.equal((res.a.output as any).stdout, '1.2.3-a');
  assert.equal((res.b.output as any).stdout, '1.2.3-b');
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
