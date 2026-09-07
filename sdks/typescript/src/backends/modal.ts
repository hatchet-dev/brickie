import { BackendSession, ExecResult, SandboxBackend } from '../backend.js';

export interface ModalOptions {
  appName?: string;
}

export function modal(options: ModalOptions = {}): SandboxBackend {
  return {
    name: 'modal',
    async createSession({ image, snapshotId, env }): Promise<BackendSession> {
      const mod = 'modal';
      const m = (await import(mod)) as any;
      const app = await m.App.lookup(options.appName ?? 'brickie', { createIfMissing: true });
      let img: any;
      if (snapshotId) {
        img = await m.Image.fromId(snapshotId);
      } else {
        img = await app.imageFromRegistry(image ?? 'alpine:3');
      }
      const sbx = await app.createSandbox(img, { env: env ?? {} });
      return {
        async exec(command): Promise<ExecResult> {
          const p = await sbx.exec(['sh', '-c', command]);
          const [stdout, stderr, exitCode] = await Promise.all([
            p.stdout.readText(),
            p.stderr.readText(),
            p.wait(),
          ]);
          return { stdout: String(stdout ?? ''), stderr: String(stderr ?? ''), exitCode: Number(exitCode ?? 0) };
        },
        async checkpoint(): Promise<string> {
          const snapImage = await sbx.snapshotFilesystem();
          const id = snapImage?.imageId ?? snapImage?.id;
          if (!id) {
            throw new Error('modal snapshotFilesystem returned no image id');
          }
          return String(id);
        },
        async destroy() {
          await sbx.terminate();
        },
      };
    },
  };
}
