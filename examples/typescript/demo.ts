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

  // One long-lived durable task is the whole session, and one sandbox lives
  // for the whole session: every turn runs inside the same micro-VM.
  const session = hatchet.durableTask({
    name: 'agent-session',
    executionTimeout: '2h',
    fn: async (input: { sessionId: string }, ctx) => {
      const image = process.env.DEMO_CLAUDE_IMAGE ? { image: process.env.DEMO_CLAUDE_IMAGE } : {};
      let sandbox = hatchet.sandbox(image);
      for (;;) {
        const { message } = await ctx.waitForEvent('turn', `input.sessionId == '${input.sessionId}'`);
        const text = String(message);
        if (text === '/exit') {
          await sandbox.destroy(ctx);
          return { done: true };
        }
        if (text.startsWith('/restore ')) {
          const from = text.split(/\s+/)[1];
          await sandbox.destroy(ctx);
          sandbox = hatchet.sandbox({ ...image, from });
          await ctx.putStream(JSON.stringify({ info: `new sandbox forked from snapshot ${from}` }));
          continue;
        }
        const turn = text.startsWith('!')
          ? await sandbox.executeCommand(ctx, text.slice(1))
          : await sandbox.executePrompt(ctx, text);
        await ctx.putStream(JSON.stringify(turn));
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
    const turn = JSON.parse((await stream.next()).value);
    if (turn.info) {
      console.log(`${turn.info}\n`);
      continue;
    }
    const out = turn.output ?? {};
    console.log(out.reply ?? out.stdout ?? '');
    if (out.stderr) console.log(out.stderr);
    console.log(`(snapshot ${turn.snapshotId} · ${breakdown(Date.now() - started, turn.timings)})\n`);
  }
}

main().catch((err) => {
  if (err?.code === 'ABORT_ERR') return;
  console.error(err);
  process.exit(1);
});
