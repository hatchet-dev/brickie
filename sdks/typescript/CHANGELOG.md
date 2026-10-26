# Changelog

All notable changes to `@hatchet-dev/sandbox`, the TypeScript SDK. The provider has its own changelog at the repository root.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Each release reads its notes from the matching section here.

## [Unreleased]

## [0.1.0] - 2026-10-27

### Added

- `hatchet.sandbox.create`, `connect`, and `list` on the Hatchet client, by importing `@hatchet-dev/sandbox`.
- `Sandbox.exec` with `timeoutMs`, returning stdout, stderr, exit code, and per-step timings; `Sandbox.kill`.
- `Sandbox.files`: read, write, writeFiles, list, exists, getInfo, makeDir, rename, copy, remove, download, upload. Files over 2 MiB are staged through the provider's object store on presigned URLs.
- `create({ id, template, from, metadata, idleTimeoutMs })` for caller-chosen ids, images, forks, and expiry.
- Every call is a Hatchet task run, and a durable step inside a durable task.
- OpenTelemetry instrumentation, off with `BRICKIE_OTEL=false`.
