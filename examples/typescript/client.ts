import { HatchetClient } from '@hatchet-dev/typescript-sdk/v1';
import { HatchetEmbeddedClient } from '@hatchet-dev/typescript-sdk/v1/embedded';

export type ExampleClient = HatchetClient & { stopEmbedded?: () => Promise<void> };

export async function initClient(): Promise<ExampleClient> {
  if (process.env.HATCHET_CLIENT_TOKEN) {
    return HatchetClient.init();
  }
  return HatchetEmbeddedClient.init({ logLevel: 'error' });
}
