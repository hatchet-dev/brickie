import { randomUUID } from 'node:crypto';
import '@hatchet-dev/sandbox';
import { initClient } from './client.js';

const opts = process.env.DEMO_TEMPLATE ? { template: process.env.DEMO_TEMPLATE } : {};

const sec = (ms = 0) => `${(ms / 1000).toFixed(2)}s`;
const step = (name: string, detail: string, t: { timings?: Record<string, number>; snapshotId?: string } = {}) => {
  const { claim, restore, exec, checkpoint } = t.timings ?? {};
  const parts = [claim && `start ${sec(claim)}`, restore && `restore ${sec(restore)}`, exec && `run ${sec(exec)}`, checkpoint && `snapshot ${sec(checkpoint)}`];
  console.log(`${name.padEnd(8)} ${detail}${t.snapshotId ? `  [snapshot ${t.snapshotId}]` : ''}  ${parts.filter(Boolean).join(' · ')}`);
};

async function main() {
  const hatchet = await initClient();

  const session = hatchet.durableTask({
    name: 'agent-session',
    executionTimeout: '30m',
    fn: async (input: { id: string }) => {
      const started = Date.now();
      const sandbox = await hatchet.sandbox.create({ ...opts, id: input.id, metadata: { tenant: 'acme', purpose: 'demo' } });
      step('create', sandbox.sandboxId, { timings: { claim: Date.now() - started } });
      const found = await hatchet.sandbox.list({ metadata: { tenant: 'acme' } });
      step('list', found.map((s) => `${s.sandboxId}${s.live ? ' (live)' : ''}`).join(' '));

      const hello = await sandbox.exec('echo hello > a.txt && cat a.txt');
      step('exec', JSON.stringify(hello.stdout), hello);

      const written = await sandbox.files.write('b.txt', '  bytes kept exact  \n');
      step('write', `${written.path} ${written.size} bytes`);
      step('read', JSON.stringify(await sandbox.files.read('b.txt')));
      step('files', (await sandbox.files.list()).map((e) => e.path).join(' '));

      const fork = await hatchet.sandbox.create({ ...opts, from: hello.snapshotId });
      const inFork = await fork.exec('ls');
      step('fork', `${fork.sandboxId} has ${inFork.stdout.split('\n').join(' ')} (no b.txt)`, inFork);
      await fork.kill();
      await sandbox.kill();
      step('kill', 'both sandboxes removed');

      const again = await hatchet.sandbox.connect(input.id, opts);
      const back = await again.exec('cat a.txt b.txt');
      step('connect', `${again.sandboxId} from its latest snapshot: ${JSON.stringify(back.stdout)}`, back);
      await again.kill();

      return { sandboxId: input.id, lastSnapshot: back.snapshotId };
    },
  });

  const worker = await hatchet.worker('demo-worker', { workflows: [session] });
  void worker.start();
  await worker.waitUntilReady();

  const id = `demo-${randomUUID().slice(0, 8)}`;
  console.log(`agent-session durable task for sandbox ${id}\n`);
  const result = await session.run({ id });
  console.log(`\ndone: ${JSON.stringify(result)}`);
  console.log(`continue it later: hatchet.sandbox.connect('${id}')`);

  await worker.stop();
  await hatchet.stopEmbedded?.();
}

main().then(
  () => process.exit(0),
  (err) => {
    console.error(err);
    process.exit(1);
  }
);
