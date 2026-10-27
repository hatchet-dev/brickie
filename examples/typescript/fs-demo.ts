import { createHash, randomBytes } from 'node:crypto';
import { FilesystemError } from '@hatchet-dev/sandbox';
import { initClient } from './client.js';

async function main() {
  const hatchet = await initClient();
  const sandbox = await hatchet.sandbox.create({
    ...(process.env.DEMO_SANDBOX_ID ? { id: process.env.DEMO_SANDBOX_ID } : {}),
    ...(process.env.DEMO_TEMPLATE ? { template: process.env.DEMO_TEMPLATE } : {}),
  });
  console.log(`sandbox ${sandbox.sandboxId}`);

  try {
    const written = await sandbox.files.write('notes/hello.txt', '  exact bytes, spaces kept  \n');
    console.log('write   ', written.path, `${written.size} bytes`);
    console.log('read    ', JSON.stringify(await sandbox.files.read('notes/hello.txt')));

    await sandbox.files.copy('notes', 'backup');
    await sandbox.files.rename('notes/hello.txt', 'notes/renamed.txt');
    for (const e of await sandbox.files.list('.', { depth: 2 })) {
      console.log('list    ', e.type.padEnd(4), e.permissions, String(e.size).padStart(5), e.path);
    }

    await sandbox.files.remove('backup');
    console.log('exists  ', await sandbox.files.exists('backup'));

    const mb = Number(process.env.DEMO_BIG_MB ?? 5);
    const big = randomBytes(mb << 20);
    const sha = (b: Uint8Array) => createHash('sha256').update(b).digest('hex').slice(0, 12);
    try {
      let t = Date.now();
      await sandbox.files.write('big.bin', big);
      const wrote = Date.now() - t;
      t = Date.now();
      const back = await sandbox.files.read('big.bin', { format: 'bytes' });
      console.log('big     ', `${mb} MiB write ${wrote}ms read ${Date.now() - t}ms`, sha(big) === sha(back) ? 'sha256 match' : 'SHA256 MISMATCH');
    } catch (err) {
      if (!(err instanceof FilesystemError)) throw err;
      console.log('big     ', `${err.code}: ${err.message}`);
    }

    try {
      await sandbox.files.read('../outside');
    } catch (err) {
      if (!(err instanceof FilesystemError)) throw err;
      console.log('error   ', err.code);
    }

    if (process.env.DEMO_DOWNLOAD_URL) {
      const got = await sandbox.files.download(process.env.DEMO_DOWNLOAD_URL, 'downloads/file');
      console.log('download', got.path, `${got.size} bytes`);
    }
  } finally {
    await sandbox.kill();
    await hatchet.stopEmbedded?.();
  }
}

main().then(
  () => process.exit(0),
  (err) => {
    console.error(err);
    process.exit(1);
  }
);
