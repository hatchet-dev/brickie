import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { after, before, test } from 'node:test';
import { HatchetClient, type JsonObject } from '@hatchet-dev/typescript-sdk/v1';
import '../src/index.js';

const hatchet = HatchetClient.init();
const template = process.env.DEMO_TEMPLATE ?? 'alpine:3';
const log = (...parts: unknown[]) => console.log('   ', ...parts);

const task = hatchet.workflow<JsonObject, Record<string, { output: JsonObject }>>({ name: 'brickie-sandbox' });
const rawTurn = async (fn: string, sandboxId: string, extra: JsonObject = {}) => {
  const r = await task.run({ fn, input: {}, sandboxId, image: template, ...extra });
  return (r['brickie-sandbox'] ?? r) as { output: JsonObject };
};

const session = `t-${randomUUID().slice(0, 8)}`;
const tags = { suite: 'sdk-e2e', session };

before(() => log('session', session, 'template', template));
after(async () => {
  const gone = await hatchet.sandbox.delete({ metadata: tags });
  log('cleanup deleted', gone);
});

test('a fork inherits the parent metadata and its own tags win', async () => {
  const parent = await hatchet.sandbox.create({ template, metadata: { ...tags, role: 'parent', owner: 'alice' } });
  const made = await parent.exec('echo one > a.txt');
  log('parent', parent.sandboxId, 'snapshot', made.snapshotId);
  assert.ok(made.snapshotId?.startsWith(`${parent.sandboxId}/`), 'snapshot id carries the sandbox id');

  const fork = await hatchet.sandbox.create({ template, from: made.snapshotId, inheritParentMetadata: true, metadata: { role: 'fork' } });
  const [info] = await hatchet.sandbox.list({ sandboxId: fork.sandboxId });
  log('fork', fork.sandboxId, 'metadata', info?.metadata);
  assert.deepEqual(info?.metadata, { ...tags, role: 'fork', owner: 'alice' });

  const plain = await hatchet.sandbox.create({ template, from: made.snapshotId, metadata: { role: 'plain' } });
  const [plainInfo] = await hatchet.sandbox.list({ sandboxId: plain.sandboxId });
  log('fork without inheritance', plain.sandboxId, 'metadata', plainInfo?.metadata);
  assert.deepEqual(plainInfo?.metadata, { role: 'plain' });
  await plain.delete();
});

test('list matches a subset of the tags', async () => {
  const found = await hatchet.sandbox.list({ metadata: { suite: 'sdk-e2e', session } });
  log('list by two tags finds', found.map((s) => `${s.sandboxId}:${s.metadata?.role}`));
  assert.deepEqual(found.map((s) => s.metadata?.role).sort(), ['fork', 'parent']);
  const forks = await hatchet.sandbox.list({ metadata: { ...tags, role: 'fork' } });
  assert.equal(forks.length, 1);
});

test('delete by metadata removes the fork and keeps the parent', async () => {
  const [parent] = await hatchet.sandbox.list({ metadata: { ...tags, role: 'parent' } });
  const deleted = await hatchet.sandbox.delete({ metadata: { ...tags, role: 'fork' } });
  log('deleted', deleted);
  assert.equal(deleted.length, 1);
  assert.deepEqual(await hatchet.sandbox.list({ sandboxId: deleted[0] }), []);

  const again = await hatchet.sandbox.connect(parent.sandboxId, { template });
  const back = await again.exec('cat a.txt');
  log('parent still connects and reads', JSON.stringify(back.stdout));
  assert.equal(back.stdout, 'one');
  await again.kill();
});

test('delete is idempotent and succeeds for an unknown sandbox', async () => {
  const sb = await hatchet.sandbox.create({ template, metadata: { ...tags, role: 'temp' } });
  await sb.exec('echo x > x.txt');
  assert.deepEqual((await rawTurn('$delete', sb.sandboxId)).output, { deleted: true });
  assert.deepEqual((await rawTurn('$delete', sb.sandboxId)).output, { deleted: true });
  assert.deepEqual((await rawTurn('$delete', `never-${randomUUID().slice(0, 8)}`)).output, { deleted: true });
  await assert.rejects(hatchet.sandbox.connect(sb.sandboxId, { template }), /unknown sandbox/);
  log('deleted twice, unknown id, connect refused');
});

test('a fork of a deleted parent fails to connect instead of starting empty', async () => {
  const parent = await hatchet.sandbox.create({ template, metadata: { ...tags, role: 'doomed' } });
  const made = await parent.exec('echo p > p.txt');
  const fork = await hatchet.sandbox.create({ template, from: made.snapshotId, inheritParentMetadata: true, metadata: { role: 'orphan' } });
  await fork.kill();
  await parent.delete();
  await assert.rejects(hatchet.sandbox.connect(fork.sandboxId, { template }), /read snapshot .*NotFound/);
  log('orphaned fork', fork.sandboxId, 'refused with NotFound');
  await fork.delete();
});

test('a snapshot id that is not <sandbox>/<run> is refused', async () => {
  await assert.rejects(hatchet.sandbox.create({ template, from: '../../etc/passwd' }), /invalid snapshot id/);
  await assert.rejects(hatchet.sandbox.create({ template, from: 'd1f2b21a-0000-4000-8000-000000000000' }), /invalid snapshot id/);
  log('legacy and escaping snapshot ids refused');
});

test('delete without a filter is refused', async () => {
  await assert.rejects(hatchet.sandbox.delete({}), /needs a sandboxId or a metadata filter/);
  await assert.rejects(hatchet.sandbox.delete({ metadata: {} }), /needs a sandboxId or a metadata filter/);
});
