import { BackendSession, ExecResult, SandboxBackend } from '../backend.js';

export interface DaytonaOptions {
  apiKey?: string;
  target?: string;
}

export function daytona(options: DaytonaOptions = {}): SandboxBackend {
  return {
    name: 'daytona',
    async createSession({ image, snapshotId, env }): Promise<BackendSession> {
      const apiKey = options.apiKey ?? process.env.DAYTONA_API_KEY;
      if (!apiKey) {
        throw new Error('DAYTONA_API_KEY is required for the daytona backend');
      }
      const mod = '@daytonaio/sdk';
      const { Daytona } = (await import(mod)) as any;
      const client = new Daytona({ apiKey, ...(options.target ? { target: options.target } : {}) });
      const createParams = snapshotId ? { snapshot: snapshotId } : image ? { image } : {};
      const sbx = await client.create({ ...createParams, envVars: env ?? {} });
      return {
        async exec(command): Promise<ExecResult> {
          const r = await sbx.process.executeCommand(command);
          return {
            stdout: String(r.result ?? r.stdout ?? ''),
            stderr: String(r.stderr ?? ''),
            exitCode: Number(r.exitCode ?? 0),
          };
        },
        async checkpoint(): Promise<string> {
          if (typeof sbx.createSnapshot !== 'function') {
            throw new Error('installed @daytonaio/sdk version does not expose sandbox.createSnapshot');
          }
          const snap = await sbx.createSnapshot(`brickie-${Date.now()}`);
          const id = snap?.name ?? snap?.id ?? snap;
          return String(id);
        },
        async destroy() {
          await sbx.delete();
        },
      };
    },
  };
}
