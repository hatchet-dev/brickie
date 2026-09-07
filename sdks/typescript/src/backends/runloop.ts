import { BackendSession, ExecResult, SandboxBackend } from '../backend.js';

export interface RunloopOptions {
  apiKey?: string;
  blueprintId?: string;
}

export function runloop(options: RunloopOptions = {}): SandboxBackend {
  return {
    name: 'runloop',
    async createSession({ image, snapshotId, env }): Promise<BackendSession> {
      const apiKey = options.apiKey ?? process.env.RUNLOOP_API_KEY;
      if (!apiKey) {
        throw new Error('RUNLOOP_API_KEY is required for the runloop backend');
      }
      const mod = '@runloop/api-client';
      const { Runloop } = (await import(mod)) as any;
      const client = new Runloop({ bearerToken: apiKey });
      const createParams = snapshotId
        ? { snapshot_id: snapshotId }
        : (image ?? options.blueprintId)
          ? { blueprint_id: image ?? options.blueprintId }
          : {};
      let dbx = await client.devboxes.create({ ...createParams, environment_variables: env ?? {} });
      const deadline = Date.now() + 5 * 60 * 1000;
      while (dbx.status !== 'running') {
        if (['failure', 'shutdown'].includes(dbx.status) || Date.now() > deadline) {
          throw new Error(`runloop devbox ${dbx.id} did not reach running (status: ${dbx.status})`);
        }
        await new Promise((r) => setTimeout(r, 2000));
        dbx = await client.devboxes.retrieve(dbx.id);
      }
      return {
        async exec(command): Promise<ExecResult> {
          const r = await client.devboxes.executeSync(dbx.id, { command });
          return {
            stdout: String(r.stdout ?? ''),
            stderr: String(r.stderr ?? ''),
            exitCode: Number(r.exit_status ?? r.exitCode ?? 0),
          };
        },
        async checkpoint(): Promise<string> {
          const snap = await client.devboxes.snapshotDisk(dbx.id, {});
          const id = snap?.id;
          if (!id) {
            throw new Error('runloop snapshotDisk returned no snapshot id');
          }
          return String(id);
        },
        async destroy() {
          await client.devboxes.shutdown(dbx.id);
        },
      };
    },
  };
}
