# Aether

Aether is a self-hosted development environment for coding agents. The public
repository contains one Go server, one Go CLI, an embedded dashboard, and the
operational guides needed to run them on your own hardware.

## Public documentation

Project documentation lives in `docs/`. Read the guides affected by a change
before editing code, and update those guides when behavior or commands change.
The repository does not contain private planning, tracker, or deployment data.

## Before changing code

1. Read the relevant public documentation and source package.
2. Reuse existing helpers and dependencies instead of adding duplicates.
3. Keep credentials, host keys, invite codes, profile homes, transcripts, and
   runtime state outside the source tree.
4. Prefer an end-to-end check over duplicate unit tests.

## Checks

```sh
make fmt-check
make vet
make test
make public-audit
```

The integration suite needs Docker and real git:

```sh
make test-integration
```

The dashboard checks run from `web/`:

```sh
bun install --frozen-lockfile
bun run typecheck
bun run test
```

## Code style

- Keep Go files below 1000 lines where practical.
- Return errors with context; do not log and swallow them.
- Comments explain constraints that the code cannot express.
- Avoid speculative configuration, abstractions, and compatibility paths.
- Keep public examples safe to copy and free of credential-shaped values.

## Repository shape

- `cmd/aether` is the client CLI.
- `cmd/aether-server` is the Linux server.
- `internal/` contains the server, protocol, storage, runtime, and dashboard
  packages.
- `web/` contains the embedded dashboard source.
- `docs/` contains public operational and protocol documentation.

`CLAUDE.md` carries the same public guidance for tools that look for that file.
