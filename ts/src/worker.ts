import { Duration, HatchetClient } from '@hatchet-dev/typescript-sdk/v1';
import { builtinFns } from './builtins.js';
import { KubernetesDriver } from './kubernetes-driver.js';
import { sandboxProvider } from './provider.js';

async function main() {
  const hatchet = HatchetClient.init();
  const credentials = (process.env.BRICKIE_CREDENTIALS ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

  const driver = new KubernetesDriver(builtinFns(), {
    namespace: process.env.BRICKIE_SANDBOX_NAMESPACE || 'default',
    image: process.env.BRICKIE_SANDBOX_IMAGE || 'alpine:3',
    credentials,
  });

  const task = sandboxProvider(hatchet, {
    driver,
    turnTimeout: (process.env.BRICKIE_TURN_TIMEOUT || '15m') as Duration,
  });

  const worker = await hatchet.worker('hatchet-sandbox-provider', { workflows: [task] });
  await worker.start();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
