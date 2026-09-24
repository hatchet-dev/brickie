import * as fs from 'node:fs';
import { HatchetClient } from '@hatchet-dev/typescript-sdk/v1';
import { HatchetEmbeddedClient } from '@hatchet-dev/typescript-sdk/v1/embedded';

export type ExampleClient = HatchetClient & { stopEmbedded?: () => Promise<void> };

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

export async function initClient(): Promise<ExampleClient> {
  loadRepoEnv();
  process.env.HATCHET_CLIENT_LOG_LEVEL = process.env.HATCHET_CLIENT_LOG_LEVEL ?? 'ERROR';
  if (process.env.HATCHET_CLIENT_TOKEN) {
    return HatchetClient.init();
  }
  return HatchetEmbeddedClient.init({ logLevel: 'error' });
}
