import type { JsonObject } from '@hatchet-dev/typescript-sdk/v1';

export type FileType = 'file' | 'dir' | 'symlink';

export interface EntryInfo {
  name: string;
  path: string;
  type: FileType;
  size: number;
  mode: number;
  permissions: string;
  modifiedTime?: Date;
  symlinkTarget?: string;
}

export type FileData = string | ArrayBuffer | Uint8Array | Blob;

export interface WriteEntry {
  path: string;
  data: FileData;
}

export interface ReadOpts {
  format?: 'text' | 'bytes' | 'blob';
}

export interface ListOpts {
  depth?: number;
}

export type FilesystemErrorCode =
  | 'not_found'
  | 'invalid_path'
  | 'invalid_url'
  | 'invalid_content'
  | 'invalid'
  | 'is_a_directory'
  | 'not_a_directory'
  | 'too_large'
  | 'transfer_failed';

/** Thrown for caller mistakes such as a missing file or a path outside the workspace. */
export class FilesystemError extends Error {
  constructor(
    readonly code: FilesystemErrorCode,
    message: string
  ) {
    super(message);
    this.name = 'FilesystemError';
  }
}

type RawEntry = Omit<EntryInfo, 'modifiedTime'> & { modifiedTime?: string };

interface FsOutput extends JsonObject {
  content?: string;
  entry?: RawEntry | null;
  entries?: RawEntry[];
  created?: boolean;
  error?: { code: FilesystemErrorCode; message: string };
}

/** @internal */
export type FsTurn = (fn: string, input: JsonObject) => Promise<FsOutput>;

const toEntry = (e: RawEntry): EntryInfo => ({
  ...e,
  modifiedTime: e.modifiedTime ? new Date(e.modifiedTime) : undefined,
});

async function toBase64(data: FileData): Promise<string> {
  if (typeof data === 'string') return Buffer.from(data, 'utf8').toString('base64');
  if (data instanceof Blob) return Buffer.from(await data.arrayBuffer()).toString('base64');
  if (data instanceof Uint8Array) return Buffer.from(data.buffer, data.byteOffset, data.byteLength).toString('base64');
  return Buffer.from(data).toString('base64');
}
/** File operations on a sandbox workspace. Paths are relative to the workspace. Each call is one Hatchet task. */
export class Files {
  /** @internal */
  constructor(private readonly turn: FsTurn) {}

  private async call(fn: string, input: JsonObject): Promise<FsOutput> {
    const out = await this.turn(`$fs.${fn}`, input);
    if (out.error) throw new FilesystemError(out.error.code, out.error.message);
    return out;
  }

  /** Reads a file as text (default), bytes, or a Blob. Files over 2 MiB must use `download` or `exec` instead. */
  read(path: string, opts?: ReadOpts & { format?: 'text' }): Promise<string>;
  read(path: string, opts: ReadOpts & { format: 'bytes' }): Promise<Uint8Array>;
  read(path: string, opts: ReadOpts & { format: 'blob' }): Promise<Blob>;
  async read(path: string, opts: ReadOpts = {}): Promise<string | Uint8Array | Blob> {
    const { content = '' } = await this.call('read', { path });
    const bytes = Buffer.from(content, 'base64');
    if (opts.format === 'bytes') return new Uint8Array(bytes);
    if (opts.format === 'blob') return new Blob([new Uint8Array(bytes)]);
    return bytes.toString('utf8');
  }

  /** Writes one file or several. Parent directories are created. Bytes are stored exactly. */
  write(path: string, data: FileData): Promise<EntryInfo>;
  write(files: WriteEntry[]): Promise<EntryInfo[]>;
  async write(pathOrFiles: string | WriteEntry[], data?: FileData): Promise<EntryInfo | EntryInfo[]> {
    if (typeof pathOrFiles !== 'string') return this.writeFiles(pathOrFiles);
    const [entry] = await this.writeFiles([{ path: pathOrFiles, data: data ?? '' }]);
    return entry;
  }

  /** Writes several files in one task. */
  async writeFiles(files: WriteEntry[]): Promise<EntryInfo[]> {
    const encoded = await Promise.all(files.map(async (f) => ({ path: f.path, content: await toBase64(f.data) })));
    const { entries = [] } = await this.call('write', { files: encoded });
    return entries.map(toEntry);
  }

  /** Lists a directory. `depth` 1 (default) is the directory itself; higher values recurse. No snapshot is made. */
  async list(path = '.', opts: ListOpts = {}): Promise<EntryInfo[]> {
    const { entries = [] } = await this.call('list', { path, depth: opts.depth ?? 1 });
    return entries.map(toEntry);
  }

  /** True if the path exists. No snapshot is made. */
  async exists(path: string): Promise<boolean> {
    const { entry } = await this.call('stat', { path });
    return entry != null;
  }

  /** Metadata of a file or directory. Throws `FilesystemError` with code `not_found` if absent. */
  async getInfo(path: string): Promise<EntryInfo> {
    const { entry } = await this.call('stat', { path });
    if (!entry) throw new FilesystemError('not_found', `${path} does not exist`);
    return toEntry(entry);
  }

  /** Creates a directory and its parents. Returns false if it already existed. */
  async makeDir(path: string): Promise<boolean> {
    const { created } = await this.call('mkdir', { path });
    return created === true;
  }

  /** Renames or moves a file or directory. */
  async rename(oldPath: string, newPath: string): Promise<EntryInfo> {
    const { entry } = await this.call('rename', { path: oldPath, newPath });
    return toEntry(entry!);
  }

  /** Copies a file or directory tree. */
  async copy(srcPath: string, dstPath: string): Promise<EntryInfo> {
    const { entry } = await this.call('copy', { path: srcPath, newPath: dstPath });
    return toEntry(entry!);
  }

  /** Deletes a file or directory tree. */
  async remove(path: string): Promise<void> {
    await this.call('remove', { path });
  }
  /** Fetches a URL into the workspace with `curl` inside the sandbox. Use for large files; the image needs `curl`. */
  async download(url: string, path: string): Promise<EntryInfo> {
    const { entry } = await this.call('download', { url, path });
    return toEntry(entry!);
  }
  /** Sends a workspace file to a URL with HTTP PUT from inside the sandbox, for example to a presigned URL. */
  async upload(path: string, url: string): Promise<void> {
    await this.call('upload', { path, url });
  }
}
