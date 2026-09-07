import { initClient } from './client.js';
import { sandbox } from '@hatchet/sandbox';

const SESSIONS = Number(process.env.SESSIONS ?? 10);
const TURNS = Number(process.env.TURNS ?? 10);

function percentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0;
  return sorted[Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length))];
}

async function main() {
  const hatchet = await initClient();
  const sbx = sandbox(hatchet);

  const session = hatchet.durableTask({
    name: 'load-session',
    executionTimeout: '2h',
    fn: async (input: { id: number }, ctx) => {
      const durations: number[] = [];
      let from: string | undefined;
      let lines = '';
      for (let i = 0; i < TURNS; i++) {
        const started = Date.now();
        const t = await sbx.executeCommand(
          ctx,
          `echo s${input.id}-t${i} >> log.txt && wc -l < log.txt`,
          from ? { from } : {}
        );
        durations.push(Date.now() - started);
        from = t.snapshotId;
        lines = (t.output as any).stdout;
      }
      return { finalLines: Number(lines), lastSnapshot: from, durations };
    },
  });

  const worker = await hatchet.worker('load-worker', { workflows: [session] });
  void worker.start();

  console.log(`starting ${SESSIONS} sessions x ${TURNS} turns = ${SESSIONS * TURNS} turns`);
  const started = Date.now();
  const results = await Promise.allSettled(
    Array.from({ length: SESSIONS }, (_v, i) => session.run({ id: i }))
  );
  const wallSeconds = (Date.now() - started) / 1000;

  const ok = results.filter(
    (r): r is PromiseFulfilledResult<any> => r.status === 'fulfilled'
  );
  const failed = results.length - ok.length;
  const wrongLineage = ok.filter((r) => r.value.finalLines !== TURNS).length;
  const allDurations = ok
    .flatMap((r) => r.value.durations as number[])
    .sort((a, b) => a - b);

  console.log('--- load test results ---');
  console.log(`sessions: ${SESSIONS} (${failed} failed, ${wrongLineage} with wrong lineage)`);
  console.log(`turns completed: ${allDurations.length} in ${wallSeconds.toFixed(1)}s (${(allDurations.length / wallSeconds).toFixed(2)} turns/s)`);
  console.log(`turn latency ms: p50=${percentile(allDurations, 50)} p90=${percentile(allDurations, 90)} p99=${percentile(allDurations, 99)} max=${allDurations[allDurations.length - 1] ?? 0}`);
  const firstHalf = allDurations.slice(0, Math.floor(allDurations.length / 2));
  console.log(`p50 first half vs all: ${percentile([...firstHalf].sort((a, b) => a - b), 50)} vs ${percentile(allDurations, 50)} (growth here would suggest chain-depth cost)`);
  for (const r of results) {
    if (r.status === 'rejected') console.error('session failure:', r.reason?.message ?? r.reason);
  }

  await worker.stop();
  await hatchet.stopEmbedded?.();
  process.exit(failed > 0 || wrongLineage > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
