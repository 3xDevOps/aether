# Member environment home

Each member has one server-owned home directory under `<data>/homes/<member>`.
Aether mounts it read-write as `$HOME` in every container that member receives,
including agent runs and environment terminal sessions. The image and
workspace checkout are still selected separately by the administrator.

## What persists

The home is the member's durable environment:

- Executables installed in `~/.local/bin`
- Vendor login state and other files written by the agent
- Profile files synced with `aether profile push`

Container files outside the home are temporary. System packages belong in an
administrator-approved image. A member's home is mounted only in that member's
containers, never in another member's.

## Setting up an agent

The dashboard and CLI list the shipped harnesses and member-defined agents.
Choose an agent once:

```sh
aether agent add <name>
```

The command tells you what to run. Open the environment terminal, install the
agent into `~/.local/bin`, and complete the vendor login there:

```sh
aether terminal
```

After setup, every container for that member sees the same executable and login
state. A member-defined agent also records its launch arguments for later runs.
The terminal command ships in this release series.

## Profile sync

Profile sync copies the declared profile files from a member's laptop into the
member home. Credential files are excluded by the harness denylist and content
scan. A profile push affects later containers, not one already running.

## Migration

On upgrade, legacy content in
`<data>/homes/<member>/<harness>/<home-relative-path>` is moved to
`<data>/homes/<member>/<home-relative-path>` for known harness names. Empty
harness directories are removed. Existing files win if two paths conflict.
Old per-workspace executable snapshots are removed. The migration is
idempotent and does not move files outside the member home root.
