import { BackendSession, ExecResult, SandboxBackend } from '../backend.js';

export interface VercelOptions {
  token?: string;
  teamId?: string;
  projectId?: string;
}

export function vercel(options: VercelOptions = {}): SandboxBackend {
  return {
    name: 'vercel',
    async createSession({ image, snapshotId, env }): Promise<BackendSession> {
      const mod = '@vercel/sandbox';
      const { Sandbox } = (await import(mod)) as any;
      const auth = {
        ...(options.token ?? process.env.VERCEL_TOKEN ? { token: options.token ?? process.env.VERCEL_TOKEN } : {}),
        ...(options.teamId ?? process.env.VERCEL_TEAM_ID ? { teamId: options.teamId ?? process.env.VERCEL_TEAM_ID } : {}),
        ...(options.projectId ?? process.env.VERCEL_PROJECT_ID
          ? { projectId: options.projectId ?? process.env.VERCEL_PROJECT_ID }
          : {}),
      };
      const source = snapshotId
        ? { type: 'snapshot', snapshotId }
        : image
          ? { type: 'image', image }
          : undefined;
      const sbx = await Sandbox.create({ ...auth, ...(source ? { source } : {}) });
      return {
        async exec(command): Promise<ExecResult> {
          const cmd = await sbx.runCommand({ cmd: 'sh', args: ['-c', command], env: env ?? {} });
          const [stdout, stderr] = await Promise.all([cmd.stdout(), cmd.stderr()]);
          return { stdout: String(stdout ?? ''), stderr: String(stderr ?? ''), exitCode: Number(cmd.exitCode ?? 0) };
        },
        async checkpoint(): Promise<string> {
          const snap = await sbx.snapshot();
          const id = snap?.snapshotId ?? snap?.id ?? snap;
          if (!id) {
            throw new Error('vercel snapshot returned no snapshot id');
          }
          return String(id);
        },
        async destroy() {
          await sbx.stop();
        },
      };
    },
  };
}
