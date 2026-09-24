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

  // Each message is one durable turn: a fresh sandbox that continues from the
  // previous turn's snapshot. The session persists as the snapshot chain.
  const turn = hatchet.durableTask({
    name: 'agent-turn',
    executionTimeout: '15m',
    fn: async (input: { message: string; from?: string }, ctx) => {
      const sandbox = hatchet.sandbox(process.env.DEMO_CLAUDE_IMAGE ? { image: process.env.DEMO_CLAUDE_IMAGE } : {});
      const opts = input.from ? { from: input.from } : {};
      return input.message.startsWith('!')
        ? sandbox.executeCommand(ctx, input.message.slice(1), opts)
        : sandbox.executePrompt(ctx, input.message, opts);
    },
  });

  const worker = await hatchet.worker('demo-worker', { workflows: [turn] });
  void worker.start();
  await worker.waitUntilReady();

  console.log('brickie session — every message runs as one durable turn in a fresh sandbox');
  console.log('  <text> Claude turn · !<cmd> shell turn · /restore <id> fork a snapshot · /exit\n');

  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  let snapshot: string | undefined;

  for (;;) {
    const line = (await rl.question('you> ')).trim();
    if (!line) continue;
    if (line === '/exit') break;
    if (line.startsWith('/restore ')) {
      snapshot = line.split(/\s+/)[1];
      console.log(`next turn starts from snapshot ${snapshot}\n`);
      continue;
    }

    const started = Date.now();
    const result = await turn.run({ message: line, from: snapshot });
    const out = result.output as any;
    console.log(out.reply ?? out.stdout ?? '');
    if (out.stderr) console.log(out.stderr);
    snapshot = result.snapshotId;
    console.log(`(snapshot ${snapshot} · ${breakdown(Date.now() - started, result.timings)})\n`);
  }

  rl.close();
  await worker.stop();
  await hatchet.stopEmbedded?.();
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
