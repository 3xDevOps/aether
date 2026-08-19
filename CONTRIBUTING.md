# Contributing to Aether

Thanks for contributing. Aether is a self-hosted development environment for
coding agents, built as a Go server, a Go CLI, and an embedded web dashboard.

## Before you write code

1. Read the relevant guide in `docs/` and the package you will change.
2. Search for an existing helper or dependency before adding one.
3. Keep credentials and local server state outside the checkout.
4. Update public documentation when commands, defaults, or behavior change.

## Toolchain

| Need | Version | Use |
| --- | --- | --- |
| Go | 1.25+ | Server and CLI |
| GNU make | any recent | Build and checks |
| Bun | 1.3+ | Dashboard build and tests |
| Docker | recent | Integration tests and server runtime |
| git | recent | Integration tests and workspace transport |

SQLite is pure Go and the project builds with `CGO_ENABLED=0`.

```sh
git clone https://github.com/3xDevOps/Aether
cd Aether
make build
```

## Checks

Run the fast checks before opening a change:

```sh
make fmt-check
make vet
make test
make public-audit
```

The integration suite uses real Docker and git:

```sh
make test-integration
```

The dashboard checks run from `web/`:

```sh
bun install --frozen-lockfile
bun run typecheck
bun run test
```

## Testing

Prefer a test that follows the real user path. Integration tests are valuable
because the project risk is the connection between SSH, git, containers,
storage, and the dashboard. Do not add tests for language behavior, trivial
getters, or the same contract at several layers.

## Code style

- Keep files focused and Go files below 1000 lines where practical.
- Wrap errors with context and return them.
- Use comments for constraints and decisions, not for restating code.
- Prefer standard-library and existing dependency functionality.
- Do not add speculative abstractions, configuration, or compatibility paths.

## Documentation

Public documentation lives in `docs/`. Root-level `README.md` and this file
are entry points and should link to the detailed guides rather than duplicate
them. Never commit host keys, invite codes, profile homes, transcripts, runtime
state, or real credentials.

## Security

Do not open public issues for sensitive reports. Use the repository's private
security reporting channel and include a reproducible explanation without
including live credentials.

## Releases

Release tags are built by the repository workflow. Keep the installer, release
asset names, checksums, and documentation synchronized when changing packaging.
