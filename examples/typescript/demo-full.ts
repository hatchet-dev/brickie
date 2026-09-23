import { initClient } from './client.js';
import '@hatchet/sandbox';

const CLAUDE_IMAGE = process.env.DEMO_CLAUDE_IMAGE ?? '643285090708.dkr.ecr.us-east-2.amazonaws.com/brickie:claude-code-0.1.0';

function section(title: string) {
  console.log(`\n=== ${title} ===`);
}

async function main() {
  const hatchet = await initClient();

  const session = hatchet.durableTask({
    name: 'demo-session',
    executionTimeout: '5m',
    scheduleTimeout: '1m',
    fn: async (_input: {}, ctx) => {
      const shell = hatchet.sandbox();
      const claude = hatchet.sandbox({ image: CLAUDE_IMAGE });

      const setup = await shell.executeCommand(
        ctx,
        'echo "brickie: durable sandbox turns on your own cluster" > notes.txt && uname -sr'
      );
      const listed = await shell.executeCommand(ctx, 'cat notes.txt && ls', { from: setup.snapshotId });

      const poem = await claude.executePrompt(
        ctx,
        'Read notes.txt and write haiku.md containing a haiku inspired by it. Reply with only the haiku.',
        { from: listed.snapshotId }
      );

      const [branchA, branchB] = await shell.executeAll(ctx, [
        { command: 'echo "branch A was here" >> haiku.md && cat haiku.md', from: poem.snapshotId },
        { command: 'echo "branch B was here" >> haiku.md && cat haiku.md', from: poem.snapshotId },
      ]);

      const restored = await shell.executeCommand(ctx, 'ls && cat notes.txt', { from: setup.snapshotId });

      return { setup, listed, poem, branchA, branchB, restored };
    },
  });

  const worker = await hatchet.worker('demo-worker', { workflows: [session] });
  void worker.start();

  console.log('running demo session on the cluster...');
  // const r = await session.run({});

  // section('1. shell sandbox (alpine:3): write state, chain a turn');
  // console.log('turn 1 output:', (r.setup.output as any).stdout);
  // console.log('turn 1 snapshot:', r.setup.snapshotId);
  // console.log('turn 2 sees turn 1 state:', (r.listed.output as any).stdout);

  // section('2. claude sandbox (different image, same workspace)');
  // console.log((r.poem.output as any).reply);
  // console.log('files:', (r.poem.output as any).files);

  // section('3. fork: two branches from one snapshot');
  // console.log('branch A:', (r.branchA.output as any).stdout);
  // console.log('branch B:', (r.branchB.output as any).stdout);
  // console.log('snapshots:', r.branchA.snapshotId, '|', r.branchB.snapshotId);

  // section('4. restore: time-travel back to turn 1');
  // console.log((r.restored.output as any).stdout);

  // console.log('\nevery turn above is a durable Hatchet task: retries, logs, and traces are in the dashboard.');

  // await worker.stop();
  // await hatchet.stopEmbedded?.();
  // process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
