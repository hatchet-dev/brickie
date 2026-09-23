import * as readline from 'node:readline/promises';
import { initClient } from './client.js';
import '@hatchet/sandbox';

const CLAUDE_IMAGE = process.env.DEMO_CLAUDE_IMAGE ?? '643285090708.dkr.ecr.us-east-2.amazonaws.com/brickie:claude-code-0.1.0';

async function main() {
  process.env.HATCHET_CLIENT_LOG_LEVEL = process.env.HATCHET_CLIENT_LOG_LEVEL ?? 'ERROR';
  const hatchet = await initClient();

  const turn = hatchet.durableTask({
    name: 'agent-turn',
    executionTimeout: '5m',
    fn: async (input: { message: string; from?: string }, ctx) => {
      const shell = hatchet.sandbox();
      const claude = hatchet.sandbox({ image: CLAUDE_IMAGE });
      const opts = input.from ? { from: input.from } : {};
      if (input.message.startsWith('!')) {
        return shell.executeCommand(ctx, input.message.slice(1), opts);
      }
      return claude.executePrompt(ctx, input.message, opts);
    },
  });

  const worker = await hatchet.worker('demo-worker', { workflows: [turn] });
  void worker.start();

  console.log('brickie session shell — every message runs one durable turn in a fresh sandbox on your cluster');
  console.log('  <text>         agent turn (Claude in its own sandbox image)');
  console.log('  !<command>     shell turn (alpine sandbox)');
  console.log('  /restore <id>  continue from any snapshot in a new sandbox');
  console.log('  /exit          quit\n');

  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  let snapshot: string | undefined;
  const clock = () => new Date().toLocaleTimeString('en-US', { hour12: false });
  const ask = async () => {
    const prompt = () => `[${clock()}] you> `;
    const tick = setInterval(() => {
      rl.setPrompt(prompt());
      rl.prompt(true);
    }, 1000);
    try {
      return await rl.question(prompt());
    } finally {
      clearInterval(tick);
    }
  };

  for (;;) {
    const line = (await ask()).trim();
    if (!line) continue;
    if (line === '/exit') break;
    if (line.startsWith('/restore ')) {
      snapshot = line.split(/\s+/)[1];
      console.log(`next turn restores snapshot ${snapshot} into a fresh sandbox\n`);
      continue;
    }
    const started = Date.now();
    const result = await turn.run({ message: line, ...(snapshot ? { from: snapshot } : {}) });
    const elapsed = ((Date.now() - started) / 1000).toFixed(1);
    const out = result.output as any;
    console.log(out.reply ?? out.stdout ?? '');
    if (out.stderr) console.log(out.stderr);
    snapshot = result.snapshotId;
    console.log(`(snapshot ${snapshot} · ${elapsed}s)\n`);
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
