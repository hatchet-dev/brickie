import { BackendSession, ExecResult, SandboxBackend } from '../backend.js';

export interface E2bOptions {
  apiKey?: string;
  template?: string;
}

export function e2b(options: E2bOptions = {}): SandboxBackend {
  return {
    name: 'e2b',
    async createSession({ image, snapshotId, env }): Promise<BackendSession> {
      const apiKey = options.apiKey ?? process.env.E2B_API_KEY;
      if (!apiKey) {
        throw new Error('E2B_API_KEY is required for the e2b backend');
      }
      const mod = 'e2b';
      const { Sandbox } = (await import(mod)) as any;
      const template = snapshotId ?? image ?? options.template ?? 'base';
      const sbx = await Sandbox.create(template, { apiKey, envs: env ?? {} });
      return {
        async exec(command): Promise<ExecResult> {
          try {
            const r = await sbx.commands.run(command);
            return { stdout: r.stdout ?? '', stderr: r.stderr ?? '', exitCode: r.exitCode ?? 0 };
          } catch (err: any) {
            if (typeof err?.exitCode === 'number') {
              return { stdout: err.stdout ?? '', stderr: err.stderr ?? String(err), exitCode: err.exitCode };
            }
            throw err;
          }
        },
        async checkpoint(): Promise<string> {
          const res = await fetch(`https://api.e2b.app/sandboxes/${sbx.sandboxId}/snapshots`, {
            method: 'POST',
            headers: { 'X-API-Key': apiKey, 'Content-Type': 'application/json' },
            body: JSON.stringify({}),
          });
          if (!res.ok) {
            throw new Error(`e2b snapshot failed (${res.status}): ${await res.text()}`);
          }
          const body = (await res.json()) as any;
          const id = body.snapshotID ?? body.snapshotId;
          if (!id) {
            throw new Error(`e2b snapshot response had no snapshot id: ${JSON.stringify(body)}`);
          }
          return String(id);
        },
        async destroy() {
          await sbx.kill();
        },
      };
    },
  };
}
