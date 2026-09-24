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

  // One long-lived durable task is the whole session. It waits for each
  // message, runs a turn in a fresh sandbox, and carries the snapshot forward.
  const session = hatchet.durableTask({
    name: 'agent-session',
    executionTimeout: '2h',
    fn: async (input: { sessionId: string }, ctx) => {
      const sandbox = hatchet.sandbox(process.env.DEMO_CLAUDE_IMAGE ? { image: process.env.DEMO_CLAUDE_IMAGE } : {});
      let from: string | undefined;
      for (;;) {
        const { message } = await ctx.waitForEvent('turn', `input.sessionId == '${input.sessionId}'`);
        const text = String(message);
        const turn = text.startsWith('!')
          ? await sandbox.executeCommand(ctx, text.slice(1), from ? { from } : {})
          : await sandbox.executePrompt(ctx, text, from ? { from } : {});
        from = turn.snapshotId;
        await ctx.putStream(JSON.stringify(turn));
      }
    },
  });

  const worker = await hatchet.worker('demo-session-worker', { workflows: [session] });
  void worker.start();
  await worker.waitUntilReady();

  const sessionId = randomUUID();
  const ref = await session.runNoWait({ sessionId });
  const stream = hatchet.runs.subscribeToStream(await ref.getWorkflowRunId());

  console.log('brickie session (one long-lived durable task) — every message is a turn in a fresh sandbox');
  console.log('  <text> Claude turn · !<cmd> shell turn · /exit\n');

  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });

  for (;;) {
    const line = (await rl.question('you> ')).trim();
    if (!line) continue;
    if (line === '/exit') break;

    const started = Date.now();
    await hatchet.events.push('turn', { sessionId, message: line });
    const turn = JSON.parse((await stream.next()).value);
    const out = turn.output ?? {};
    console.log(out.reply ?? out.stdout ?? '');
    if (out.stderr) console.log(out.stderr);
    console.log(`(snapshot ${turn.snapshotId} · ${breakdown(Date.now() - started, turn.timings)})\n`);
  }

  rl.close();
  await ref.cancel();
  await worker.stop();
  await hatchet.stopEmbedded?.();
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
