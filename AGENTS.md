# Aether

Aether is a self-hosted development environment for coding agents. The public
repository contains one Go server, one Go CLI, an embedded dashboard, and the
operational guides needed to run them on your own hardware.

## Public documentation

Project documentation lives in `docs/`. Read the guides affected by a change
before editing code, and update those guides when behavior or commands change.
The repository does not contain private planning, tracker, or deployment data.

Docs carry no fluff. Every sentence earns its place; cut filler, marketing
prose, and repetition.

## Audience

The reader is a software developer who is new to Aether. Assume working
fluency with git, a shell, containers, and coding-agent CLIs such as Claude
Code, Codex, and opencode. Do not assume any knowledge of Aether's own
concepts - workspace, run, harness, tool snapshot - and introduce each one
where it first appears.

In practice, across docs, CLI output, and dashboard copy:

- Give the exact command, and name the file or flag it touches.
- Show the real error. Never hide it behind a friendlier message.
- Keep the no-fluff rule above. Explain a concept once, then use it.
- Make every surface obvious on first contact, never by taking away
  developer control.

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
make lint
make test
make test-scripts
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

- Write only the code the change needs. No dead code, single-use helpers,
  flags nobody sets, branches for cases that cannot happen, or "while I'm
  here" edits.
- Comment only when the code cannot say it: a constraint, an invariant, or a
  reason that is not obvious from the diff. Never restate what the code does.
  How something works, why it is designed that way, and how to operate it
  belong in `docs/`, not in comments.
- Keep Go files below 1000 lines where practical.
- Return errors with context; do not log and swallow them.
- Avoid speculative configuration, abstractions, and compatibility paths.
- Keep public examples safe to copy and free of credential-shaped values.

## Commits

Commit messages and pull request titles follow
[Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/). Pull
requests are squash-merged, so the PR title becomes the commit on `main` and
must follow the same rules.

```
type(scope): summary

What was wrong before, what happens now, and why this approach.

Fixes #123
```

Header:

- `type` is one of `feat` (new user-visible capability), `fix` (corrects
  wrong behavior), `docs`, `refactor` (no behavior change), `perf`, `test`,
  `build` (Makefile, `go.mod`, packaging, desktop shell), `ci`, `chore`
  (housekeeping that ships no code change), or `revert`.
- `scope` names what changed: `cli`, `server`, `dashboard`, `desktop`,
  `docs`, or an `internal/` package such as `scheduler` or `syncd`. Omit it
  when the change spans the repository.
- The summary is imperative and lowercase, has no trailing period, and fits
  in 72 characters. It completes the sentence "this commit will ...". Say
  what the user gets, not which files moved.
- Mark a breaking change with `!` before the colon and a `BREAKING CHANGE:`
  footer that states the migration.

Body and footer:

- Add a body whenever the summary alone does not explain why. Wrap it at 72
  characters. Leave out how; the diff shows that.
- Reference issues with `Fixes #n` or `Closes #n`. A revert uses
  `revert: <original header>` and `This reverts commit <sha>.` in the body.

One commit, one type. A summary that needs "and" is two commits. Never
`wip`, `update`, `fix stuff`, `misc`, or emoji.

```
feat(cli): add aether gui build to package the desktop app
fix(scheduler): reattach to surviving containers after a server restart
docs(quickstart): explain how to get the desktop app
refactor(store): collapse sessions into workspaces
feat(cli)!: drop the aether dash command
```

## Repository shape

- `cmd/aether` is the client CLI.
- `cmd/aether-server` is the Linux server.
- `internal/` contains the server, protocol, storage, runtime, and dashboard
  packages.
- `web/` contains the embedded dashboard source.
- `docs/` contains public operational and protocol documentation.

`CLAUDE.md` carries the same public guidance for tools that look for that file.
