import { Readable, Writable } from 'node:stream';
import {
  CoreV1Api,
  CustomObjectsApi,
  Exec,
  KubeConfig,
  V1Status,
} from '@kubernetes/client-node';
import { COMMAND_FN } from './builtins.js';
import { LocalTurnFn, RUNNER } from './local-driver.js';
import { SandboxDriver, TurnRequest, TurnResponse } from './provider.js';

const GROUP = 'agents.x-k8s.io';
const VERSION = 'v1beta1';
const PLURAL = 'sandboxes';
const CONTAINER = 'sandbox';
const WORKSPACE = '/workspace';

export interface KubernetesDriverOpts {
  context?: string;
  namespace?: string;
  image?: string;
  credentials?: string[];
  readyTimeoutMs?: number;
}

export class KubernetesDriver implements SandboxDriver {
  private namespace: string;
  private image: string;
  private credentials: string[];
  private readyTimeoutMs: number;
  private core: CoreV1Api;
  private crd: CustomObjectsApi;
  private execClient: Exec;

  constructor(
    private fns: Record<string, LocalTurnFn>,
    opts: KubernetesDriverOpts = {}
  ) {
    this.namespace = opts.namespace ?? 'default';
    this.image = opts.image ?? 'alpine:3';
    this.credentials = opts.credentials ?? [];
    this.readyTimeoutMs = opts.readyTimeoutMs ?? 300_000;
    const kc = new KubeConfig();
    kc.loadFromDefault();
    if (opts.context) {
      kc.setCurrentContext(opts.context);
    }
    this.core = kc.makeApiClient(CoreV1Api);
    this.crd = kc.makeApiClient(CustomObjectsApi);
    this.execClient = new Exec(kc);
  }

  private injectedEnv(): Array<{ name: string; value: string }> {
    return this.credentials
      .filter((name) => process.env[name] !== undefined)
      .map((name) => ({ name, value: process.env[name] as string }));
  }

  private async exec(
    pod: string,
    command: string[],
    stdin?: Buffer
  ): Promise<{ stdout: Buffer; stderr: string }> {
    const stdoutChunks: Buffer[] = [];
    let stderr = '';
    const stdout = new Writable({
      write(chunk, _enc, cb) {
        stdoutChunks.push(Buffer.from(chunk));
        cb();
      },
    });
    const stderrStream = new Writable({
      write(chunk, _enc, cb) {
        stderr += chunk.toString();
        cb();
      },
    });
    await new Promise<void>((resolve, reject) => {
      this.execClient
        .exec(
          this.namespace,
          pod,
          CONTAINER,
          command,
          stdout,
          stderrStream,
          stdin ? Readable.from(stdin) : null,
          false,
          (status: V1Status) => {
            if (status.status === 'Success') {
              resolve();
            } else {
              reject(new Error(`exec ${command.join(' ')} failed: ${status.message ?? ''}\n${stderr}`));
            }
          }
        )
        .catch(reject);
    });
    return { stdout: Buffer.concat(stdoutChunks), stderr };
  }

  private async waitReady(pod: string): Promise<void> {
    const deadline = Date.now() + this.readyTimeoutMs;
    while (Date.now() < deadline) {
      try {
        const res = await this.core.readNamespacedPod({ name: pod, namespace: this.namespace });
        const ready = res.status?.containerStatuses?.every((c) => c.ready) ?? false;
        if (res.status?.phase === 'Running' && ready) {
          return;
        }
      } catch {}
      await new Promise((r) => setTimeout(r, 2000));
    }
    throw new Error(`sandbox pod ${pod} not ready within ${this.readyTimeoutMs}ms`);
  }

  async runTurn(req: TurnRequest): Promise<TurnResponse> {
    const fn = this.fns[req.fn];
    if (!fn && req.fn !== COMMAND_FN) {
      throw new Error(`unknown sandbox fn: ${req.fn}`);
    }
    const name = req.sandboxName;
    await this.crd.createNamespacedCustomObject({
      group: GROUP,
      version: VERSION,
      namespace: this.namespace,
      plural: PLURAL,
      body: {
        apiVersion: `${GROUP}/${VERSION}`,
        kind: 'Sandbox',
        metadata: { name },
        spec: {
          podTemplate: {
            spec: {
              containers: [
                {
                  name: CONTAINER,
                  image: req.image ?? this.image,
                  command: ['sleep', 'infinity'],
                  env: this.injectedEnv(),
                  workingDir: WORKSPACE,
                },
              ],
            },
          },
        },
      },
    });
    try {
      await this.waitReady(name);
      await this.exec(name, ['mkdir', '-p', WORKSPACE]);
      if (req.snapshotTar) {
        await this.exec(name, ['tar', '-xzf', '-', '-C', WORKSPACE], req.snapshotTar);
      }
      let output: TurnResponse['output'];
      if (req.fn === COMMAND_FN) {
        const res = await this.exec(name, [
          'sh',
          '-c',
          `cd ${WORKSPACE} && { ${String(req.input.cmd)}
}; printf '__BRICKIE_EXIT__%s' $?`,
        ]);
        const raw = res.stdout.toString('utf8');
        const match = raw.match(/__BRICKIE_EXIT__(\d+)$/);
        output = {
          stdout: raw.replace(/__BRICKIE_EXIT__\d+$/, '').trim(),
          stderr: res.stderr.trim(),
          exitCode: match ? Number(match[1]) : -1,
        };
      } else {
        await this.exec(name, ['mkdir', '-p', `${WORKSPACE}/.brickie`]);
        await this.exec(
          name,
          ['sh', '-c', `cat > ${WORKSPACE}/.brickie/runner.cjs`],
          Buffer.from(RUNNER)
        );
        await this.exec(
          name,
          ['sh', '-c', `cat > ${WORKSPACE}/.brickie/input.json`],
          Buffer.from(JSON.stringify({ fnSource: fn!.toString(), input: req.input }))
        );
        await this.exec(name, ['node', `${WORKSPACE}/.brickie/runner.cjs`]);
        const outputRaw = await this.exec(name, ['cat', `${WORKSPACE}/.brickie/output.json`]);
        output = JSON.parse(outputRaw.stdout.toString('utf8'));
        await this.exec(name, ['rm', '-rf', `${WORKSPACE}/.brickie`]);
      }
      const tar = await this.exec(name, ['tar', '-czf', '-', '-C', WORKSPACE, '.']);
      return { output, snapshotTar: tar.stdout };
    } finally {
      await this.crd
        .deleteNamespacedCustomObject({
          group: GROUP,
          version: VERSION,
          namespace: this.namespace,
          plural: PLURAL,
          name,
        })
        .catch(() => {});
    }
  }
}
