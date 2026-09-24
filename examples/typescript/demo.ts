import { randomUUID } from 'node:crypto';
import * as fs from 'node:fs';
import * as readline from 'node:readline/promises';
import { initClient } from './client.js';
import '@hatchet/sandbox';

function loadRepoEnv() {
  for (const envPath of ['.env', '../.env', '../../.env']) {
    let text = '';
    try {
      text = fs.readFileSync(envPath, 'utf8');
    } catch {
      continue;
    }
    for (const line of text.split('\n')) {
      const eq = line.indexOf('=');
      if (eq <= 0 || line.startsWith('#')) continue;
      const key = line.slice(0, eq).trim();
      process.env[key] = process.env[key] ?? line.slice(eq + 1).trim();
    }
    return;
  }
}

const CLAUDE_IMAGE = process.env.DEMO_CLAUDE_IMAGE;

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
  loadRepoEnv();
  process.env.HATCHET_CLIENT_LOG_LEVEL = process.env.HATCHET_CLIENT_LOG_LEVEL ?? 'ERROR';
  if (process.env.HATCHET_CLIENT_TOKEN) {
    console.log('hatchet: using the instance from HATCHET_CLIENT_TOKEN');
  } else {
    console.log('hatchet: no token found; starting an embedded engine (runs are not on your dashboard)');
  }
  const hatchet = await initClient();

  const session = hatchet.durableTask({
    name: 'agent-session',
    executionTimeout: '2h',
    fn: async (input: { sessionId: string }, ctx) => {
      const shell = hatchet.sandbox();
      const claude = hatchet.sandbox(CLAUDE_IMAGE ? { image: CLAUDE_IMAGE } : {});
      let snapshot: string | undefined;
      let turns = 0;
      for (;;) {
        const ev = await ctx.waitForEvent('session-message', `input.sessionId == '${input.sessionId}'`);
        const payload = (ev as any)['session-message'] ?? ev;
        const message = String(payload.message ?? '');
        if (message === '/exit') break;
        if (message.startsWith('/restore ')) {
          snapshot = message.split(/\s+/)[1];
          await ctx.putStream(JSON.stringify({ info: `next turn restores snapshot ${snapshot} into a fresh sandbox` }));
          continue;
        }
        const opts = snapshot ? { from: snapshot } : {};
        const result = message.startsWith('!')
          ? await shell.executeCommand(ctx, message.slice(1), opts)
          : await claude.executePrompt(ctx, message, opts);
        snapshot = result.snapshotId;
        turns += 1;
        await ctx.putStream(JSON.stringify({ output: result.output, snapshotId: snapshot, timings: result.timings }));
      }
      return { turns, snapshotId: snapshot };
    },
  });

  const worker = await hatchet.worker('demo-worker', { workflows: [session] });
  void worker.start();

  const sessionId = randomUUID();
  const ref = await session.runNoWait({ sessionId });
  const runId = await ref.getWorkflowRunId();

  const replies: any[] = [];
  let wakeup: (() => void) | undefined;
  void (async () => {
    for await (const chunk of hatchet.runs.subscribeToStream(runId)) {
      try {
        replies.push(JSON.parse(chunk));
      } catch {
        continue;
      }
      wakeup?.();
    }
  })();
  const nextReply = async () => {
    while (replies.length === 0) {
      await new Promise<void>((resolve) => {
        wakeup = resolve;
      });
    }
    return replies.shift();
  };

  console.log('brickie session — one durable agent-session task; every message is a turn in a fresh sandbox');
  console.log('  <text>         agent turn (Claude)');
  console.log('  !<command>     shell turn');
  console.log('  /restore <id>  continue from any snapshot in a new sandbox');
  console.log('  /exit          end the session\n');
  console.log(`session run ${runId}\n`);

  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const clock = () => new Date().toLocaleTimeString('en-US', { hour12: false });
  const ask = async () => {
    const prompt = () => `[${clock()}] you> `;
    const tick = setInterval(() => {
      rl.setPrompt(prompt());
      rl.prompt(true);
    }, 1000);
    try {
      return await rl.question(prompt()).catch(() => new Promise<string>(() => {}));
    } finally {
      clearInterval(tick);
    }
  };

  const send = (message: string) => hatchet.events.push('session-message', { sessionId, message });

  const endSession = async () => {
    await send('/exit');
    const summary = await ref.output;
    console.log(`session ended after ${(summary as any).turns} turns`);
    rl.close();
    await worker.stop();
    await hatchet.stopEmbedded?.();
    process.exit(0);
  };

  let interrupted = false;
  rl.on('SIGINT', () => {
    if (interrupted) {
      process.exit(130);
    }
    interrupted = true;
    console.log('\nending session (ctrl+c again to force quit)');
    void endSession();
  });

  for (;;) {
    const line = (await ask()).trim();
    if (!line) continue;
    if (line === '/exit') {
      await endSession();
    }
    const started = Date.now();
    await send(line);
    const reply = await nextReply();
    if (reply.info) {
      console.log(`${reply.info}\n`);
      continue;
    }
    const out = reply.output ?? {};
    console.log(out.reply ?? out.stdout ?? '');
    if (out.stderr) console.log(out.stderr);
    console.log(`(snapshot ${reply.snapshotId} · ${breakdown(Date.now() - started, reply.timings)})\n`);
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
