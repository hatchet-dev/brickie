import assert from 'node:assert';
import * as child_process from 'node:child_process';
import * as fs from 'node:fs';
import Anthropic from '@anthropic-ai/sdk';
import { initClient } from './client.js';
import { sandbox } from '@hatchet/sandbox';

const SETUP = `managed-agent example requires a Claude Managed Agents self-hosted environment:
  1. create one: ant beta:environments create <<< '{"name":"brickie-byoc","config":{"type":"self_hosted"}}'
  2. generate an environment key for it in the Claude Console (environment page)
  3. export ANTHROPIC_ENVIRONMENT_ID=env_... ANTHROPIC_ENVIRONMENT_KEY=sk-ant-oat01-...
  4. run the brickie provider with both injected into sandboxes:
     --credentials ANTHROPIC_ENVIRONMENT_KEY,ANTHROPIC_ENVIRONMENT_ID (helm: sandbox.credentials)
  5. export ANTHROPIC_API_KEY for the control plane (session and agent management)`;

async function main() {
  const environmentId = process.env.ANTHROPIC_ENVIRONMENT_ID;
  if (!environmentId || !process.env.ANTHROPIC_ENVIRONMENT_KEY) {
    console.error(SETUP);
    process.exit(1);
  }

  const anthropic = new Anthropic();
  const hatchet = await initClient();
  const sbx = sandbox(hatchet, { image: 'node:22' });

  const agentTurn = sbx.createFunction('managedAgentTurn', async (_input, sbContext) => {
    const envId = process.env.ANTHROPIC_ENVIRONMENT_ID;
    const envKey = process.env.ANTHROPIC_ENVIRONMENT_KEY;
    if (!envId || !envKey) {
      throw new Error(
        'ANTHROPIC_ENVIRONMENT_ID and ANTHROPIC_ENVIRONMENT_KEY are not present in this sandbox: add them to the provider credentials (--credentials or helm sandbox.credentials)'
      );
    }
    if (!fs.existsSync(`${sbContext.workspace}/node_modules/@anthropic-ai/sdk`)) {
      child_process.execSync('npm init -y && npm install --no-audit --no-fund @anthropic-ai/sdk', {
        cwd: sbContext.workspace,
        stdio: 'ignore',
      });
    }
    const sdkName = '@anthropic-ai/sdk';
    const { default: AnthropicSdk } = (await import(sdkName)) as any;
    const client = new AnthropicSdk({ authToken: envKey });
    const handled: string[] = [];
    const deadline = Date.now() + 120_000;
    while (handled.length === 0 && Date.now() < deadline) {
      const poller = client.beta.environments.work.poller({
        environmentId: envId,
        environmentKey: envKey,
        blockMs: 999,
        reclaimOlderThanMs: 2000,
        drain: true,
        autoStop: false,
      });
      for await (const work of poller) {
        await client.beta.environments.work.worker({ workdir: sbContext.workspace }).handleItem({
          workId: work.id,
          environmentId: envId,
          sessionId: work.data.id,
          environmentKey: envKey,
          workSecret: work.secret,
        });
        handled.push(work.data.id);
      }
    }
    if (handled.length === 0) {
      throw new Error('no managed-agent work arrived for this environment within 120s');
    }
    const files = fs
      .readdirSync(sbContext.workspace)
      .filter((f) => !['.brickie', 'node_modules', 'package.json', 'package-lock.json'].includes(f));
    return { handled, files };
  });

  const turnTask = hatchet.durableTask({
    name: 'managed-agent-turn',
    executionTimeout: '30m',
    fn: (input: { from?: string }, ctx) =>
      agentTurn.run(ctx, {}, input.from ? { from: input.from } : {}),
  });

  const worker = await hatchet.worker('managed-agent-worker', { workflows: [turnTask] });
  void worker.start();

  let agentId = process.env.BRICKIE_MANAGED_AGENT_ID;
  if (!agentId) {
    const agent = await anthropic.beta.agents.create({
      name: 'brickie-byoc-agent',
      model: 'claude-opus-5',
      system:
        'You are a coding agent. Work only inside your working directory. When asked to reply with a word, reply with exactly that word.',
      tools: [{ type: 'agent_toolset_20260401', default_config: { enabled: true } }],
    });
    agentId = agent.id;
    console.log(`created managed agent ${agentId} (export BRICKIE_MANAGED_AGENT_ID=${agentId} to reuse it)`);
  }

  const session = await anthropic.beta.sessions.create({
    agent: agentId,
    environment_id: environmentId,
    title: 'brickie BYOC session',
  });
  console.log(`managed session ${session.id}`);

  const streamUntilIdle = async () => {
    const stream = await anthropic.beta.sessions.events.stream(session.id);
    let reply = '';
    for await (const event of stream) {
      if (event.type === 'agent.message') {
        for (const block of event.content) {
          if (block.type === 'text') {
            reply += block.text;
          }
        }
      } else if (event.type === 'session.status_idle' || event.type === 'session.status_terminated') {
        break;
      }
    }
    return reply;
  };

  const runManagedTurn = async (text: string, from?: string) => {
    const [reply, turn] = await Promise.all([
      streamUntilIdle(),
      (async () => {
        await anthropic.beta.sessions.events.send(session.id, {
          events: [{ type: 'user.message', content: [{ type: 'text', text }] }],
        });
        return turnTask.run(from ? { from } : {});
      })(),
    ]);
    return { reply, turn };
  };

  const t1 = await runManagedTurn(
    'Create notes.md containing a haiku about durable execution, then reply with exactly: done'
  );
  console.log('turn 1 reply:', t1.reply);
  const t2 = await runManagedTurn(
    'Append the line "reviewed" to notes.md, then reply with exactly: done',
    t1.turn.snapshotId
  );
  console.log('turn 2 reply:', t2.reply);

  assert.ok((t1.turn.output.files as string[]).includes('notes.md'));
  assert.ok((t2.turn.output.files as string[]).includes('notes.md'));
  assert.notEqual(t1.turn.snapshotId, t2.turn.snapshotId);
  console.log('managed-agent e2e ok');

  await worker.stop();
  await hatchet.stopEmbedded?.();
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
