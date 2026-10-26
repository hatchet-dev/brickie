# Changelog

All notable changes to the brickie provider. The TypeScript SDK has its own changelog in `sdks/typescript/CHANGELOG.md`.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Each release reads its notes from the matching section here.

## [Unreleased]

## [0.1.0] - 2026-10-27

### Added

- `brickie worker`, the sandbox provider, running as a Hatchet gRPC operator for the `brickie-sandbox` task.
- Docker driver: one container per sandbox on the local engine, `sandboxd` injected as the process, commands over gRPC with timeouts that kill the whole process group.
- Workspace snapshots after every mutating call, keyed by run id, with a latest-snapshot pointer per sandbox so `connect` resumes a sandbox by id and `create({ from })` forks any snapshot.
- File functions on the workspace: read, write, list, stat, mkdir, rename, copy, remove, download, upload; files larger than `--max-file-bytes` go through `--object-store`.
- Idle expiry per sandbox with `--sandbox-idle-ttl` and a per-create `idleTimeoutMs` bounded by `--max-sandbox-idle-ttl`.
- Sandbox metadata and `list` with metadata filters.
- Release binaries for Linux and macOS on amd64 and arm64, macOS binaries signed and notarized.
- `brickie version`, printing the release version, commit, build date, Go version, platform, and FIPS status.
- Release binaries are linked against the CMVP-certified Go Cryptographic Module v1.0.0 with FIPS 140-3 mode on by default; the release fails if a binary is not.
