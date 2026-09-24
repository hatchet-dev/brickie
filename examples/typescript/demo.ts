import { randomUUID } from 'node:crypto';
import * as readline from 'node:readline/promises';
import '@hatchet/sandbox';
import { initClient } from './client.js';

const sec = (ms: number) => `${(ms / 1000).toFixed(1)}s`;

function breakdown(totalMs: number, timings: Record<string, number> = {}) {
  const { claim = 0, restore = 0, exec = 0, checkpoint = 0, llm = 0 } = timings;
  const parts = [`total ${sec(totalMs)}`];
  if (claim) parts.push(`sandbox ${sec(claim + restore)}`);
  if (exec) parts.push(llm ? `agent ${sec(exec - llm)} · llm ${sec(llm)}` : `run ${sec(exec)}`);
  if (checkpoint) parts.push(`snapshot ${sec(checkpoint)}`);
  const hatchet = totalMs - claim - restore - exec - checkpoint;
  if (hatchet > 0 && (claim || exec)) parts.push(`hatchet ${sec(hatchet)}`);
  return parts.join(' · ');
}

async function main() {
  const hatchet = await initClient();

  const session = hatchet.durableTask({
    name: 'agent-session',
    executionTimeout: '2h',
    fn: async (input: { sessionId: string }, ctx) => {
      const opts = process.env.DEMO_TEMPLATE ? { template: process.env.DEMO_TEMPLATE } : {};
      let sandbox = await hatchet.sandbox.create(ctx, opts);
      for (;;) {
        const { message } = await ctx.waitForEvent('turn', `input.sessionId == '${input.sessionId}'`);
        const text = String(message);
        if (text === '/exit') {
          await sandbox.kill();
          return { done: true };
        }
        if (text.startsWith('/restore ')) {
          const from = text.split(/\s+/)[1];
          await sandbox.kill();
          sandbox = await hatchet.sandbox.create(ctx, { ...opts, from });
          await ctx.putStream(JSON.stringify({ info: `new sandbox ${sandbox.sandboxId} forked from snapshot ${from}` }));
          continue;
        }
        const result = text.startsWith('!') ? await sandbox.commands.run(text.slice(1)) : await sandbox.prompt(text);
        await ctx.putStream(JSON.stringify(result));
      }
    },
  });

  const worker = await hatchet.worker('demo-worker', { workflows: [session] });
  void worker.start();
  await worker.waitUntilReady();

  const sessionId = randomUUID();
  const ref = await session.runNoWait({ sessionId });
  const stream = hatchet.runs.subscribeToStream(await ref.getWorkflowRunId());

  console.log('brickie session — one durable task, one sandbox, every message is a turn inside it');
  console.log('  <text> Claude turn · !<cmd> shell turn · /restore <id> fork a snapshot · /exit\n');

  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });

  const end = async () => {
    await hatchet.events.push('turn', { sessionId, message: '/exit' });
    await ref.output;
    rl.close();
    await worker.stop();
    await hatchet.stopEmbedded?.();
    process.exit(0);
  };
  let interrupted = false;
  rl.on('SIGINT', () => {
    if (interrupted) process.exit(130);
    interrupted = true;
    console.log('\nending session (ctrl+c again to force quit)');
    void end();
  });

  for (;;) {
    const line = (await rl.question('you> ')).trim();
    if (!line) continue;
    if (line === '/exit') await end();

    const started = Date.now();
    await hatchet.events.push('turn', { sessionId, message: line });
    const result = JSON.parse((await stream.next()).value);
    if (result.info) {
      console.log(`${result.info}\n`);
      continue;
    }
    console.log(result.reply ?? result.stdout ?? '');
    if (result.stderr) console.log(result.stderr);
    console.log(`(snapshot ${result.snapshotId} · ${breakdown(Date.now() - started, result.timings)})\n`);
  }
}

main().catch((err) => {
  if (err?.code === 'ABORT_ERR') return;
  console.error(err);
  process.exit(1);
});
