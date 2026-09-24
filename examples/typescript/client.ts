import { spawn } from 'node:child_process';
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

// Without a token the embedded engine runs in-process and nobody else starts
// the sandbox provider, so the example does.
function startLocalProvider(hatchet: HatchetClient) {
  const cfg = hatchet.config;
  const child = spawn(process.env.BRICKIE_BIN ?? 'brickie', ['worker'], {
    stdio: ['ignore', 'inherit', 'inherit'],
    env: {
      ...process.env,
      HATCHET_CLIENT_TOKEN: cfg.token,
      HATCHET_CLIENT_HOST_PORT: cfg.host_port,
      HATCHET_CLIENT_TLS_STRATEGY: cfg.tls_config?.tls_strategy ?? 'none',
    },
  });
  child.on('error', (err) => console.error(`brickie: failed to start the sandbox provider: ${err.message} (set BRICKIE_BIN)`));
  process.on('exit', () => child.kill());
}

export async function initClient(): Promise<ExampleClient> {
  loadRepoEnv();
  process.env.HATCHET_CLIENT_LOG_LEVEL = process.env.HATCHET_CLIENT_LOG_LEVEL ?? 'ERROR';
  if (process.env.HATCHET_CLIENT_TOKEN) {
    return HatchetClient.init();
  }
  const hatchet = await HatchetEmbeddedClient.init({ logLevel: 'error' });
  startLocalProvider(hatchet);
  return hatchet;
}
