# Workspace bootstrap and tool snapshots

This guide covers the persistent workspace environment used by bootstrap shells,
harness login, and agent runs. It is intentionally agent agnostic. Aether does
not contain vendor installer logic.

## Vocabulary

- **Image**: a read-only container filesystem selected by the administrator.
  A workspace uses the server's neutral image unless its administrator sets a
  custom image.
- **Container**: one runtime instance of an image. Bootstrap shells, login
  shells, and runs are separate containers and are destroyed when their work is
  complete.
- **Workspace environment**: the server-owned definition that combines the
  image, user, environment, setup policy, tools, profile, credentials, and
  checkout mounts.
- **Tool snapshot**: an immutable, content-addressed copy of one member's
  user-local tools for one workspace. It is not a profile snapshot and is not a
  credential home.
- **Profile snapshot**: a separately managed copy of the declared harness
  profile. It is pinned and materialized independently from tools and login
  state.
- **Credential home**: the per-member, per-harness login state mounted according
  to the administrator's harness definition. It is never stored in a tool
  snapshot.

## First bootstrap

A workspace administrator creates the workspace. An empty image selection uses
the server-configured neutral image:

```sh
aether workspace init <workspace>
```

A member then opens the bootstrap shell and optionally names the executable that
must be present when the shell exits:

```sh
aether workspace bootstrap <workspace> --command <executable>
```

Inside the terminal, install the executable using the vendor's documented
procedure. The shell is inside a server-created container. Its `~/.local` is a
private staging directory for this member and workspace, mounted read-write only
for this bootstrap session. Do not put credentials in installer arguments or
shell history.

On a clean exit, Aether checks the requested executable, digests the staging
tree, promotes it to an immutable snapshot, and activates that snapshot for the
member and workspace. A failed check does not replace the previous active
snapshot. If no `--command` is supplied, the snapshot can still be created, but
there is no executable check at shell exit.

The OMP name is a useful generic example, but it is not built into Aether:

```sh
aether workspace bootstrap <workspace> --command omp
# In the shell, follow the OMP project's official installation instructions.
# Then exit the shell so Aether can verify and snapshot ~/.local.
```

Installing an executable is separate from making it launchable. An
administrator must register a validated generic harness definition for `omp`
before a member can run `aether run ... --agent omp`. The definition supplies
argv templates and explicit profile and credential paths. It does not install
OMP or infer secret paths from its name. See [harnesses.md](harnesses.md).

## Verify, inspect, rollback, and reset

The control commands expose metadata only. They do not print host paths,
environment contents, or credentials:

| Command | Purpose |
| --- | --- |
| `aether workspace tools list <workspace>` | List active and retained snapshots with ID, active state, executable, version, and creation time. |
| `aether workspace tools verify <workspace> --command <executable>` | Report active metadata and whether the executable matches its recorded manifest. |
| `aether workspace tools rollback <workspace> <snapshot>` | Make a retained snapshot the active head for future shells and runs. |
| `aether workspace tools reset <workspace> --confirm` | Remove pending staging and removable retained snapshots, then clear the active head. |

A rollback does not alter a running container. Runs pin their complete
environment plan, including the tool snapshot, before the container is created.
A later bootstrap, rollback, or reset affects only future provisioning.

Reset requires explicit confirmation. A snapshot referenced by a running run or
pending bootstrap is retained and cannot be deleted until that reference is
released. If reset cannot remove all requested state, keep the snapshot ID and
retry after the run or shell has ended.

## Login and run mounts

Login is a separate shell mode, selected by the public command below:

```sh
aether setup <harness> --workspace <workspace>
```

The login shell uses the active tool snapshot read-only at the user's `~/.local`
and mounts the selected harness credential home according to administrator
policy. Profile snapshots are a third, separate mount and are not merged into
tools or credentials. Login state is scoped to one member and harness, while
tool snapshots are scoped to one member and workspace.

Normal runs mount the pinned tool snapshot read-only. They cannot mutate the
snapshot or another member's staging directory. User-local tools persist across
containers; system packages installed under `/usr` or `/etc`, edits elsewhere
in the container filesystem, and a container's process state do not persist.
Put required system dependencies in an administrator-approved custom image.

## Image escape hatch

The neutral image contains universal shell/runtime dependencies, not a vendor
agent. A custom image is useful when a project needs system packages,
non-standard users, or a prebuilt base. Only an administrator chooses the
workspace image. Members cannot replace it through a shell request.

To prepare ordinary artifacts for administrator review and image publication:

```sh
aether image init
aether image init --devcontainer
```

These commands create a normal `Dockerfile`, `.dockerignore`, and optionally a
standard `.devcontainer/devcontainer.json`. They do not build an image, log in
to a registry, or add a vendor-specific installer. A deployment may pin the
approved image with the server's `--neutral-image` setting. Aether does not
silently execute arbitrary project container metadata on the server.

## Isolation and recovery boundaries

Every shell is a container operation. The server never opens a shell on the
host, and workspace containers never receive the Docker socket. Host paths for
tools, profiles, and credentials are derived from server-owned data roots and
are validated before mounting.

A bootstrap disconnect stops and destroys the shell container but keeps the
pending staging metadata and directory for this member and workspace. Reconnect
and resume it with:

```sh
aether workspace bootstrap <workspace> --resume --command <executable>
```

Use `--reset` on the bootstrap command to discard pending staging instead of
resuming it:

```sh
aether workspace bootstrap <workspace> --reset
```

The server also removes abandoned staging trees that have no pending session
under its bounded cleanup policy. A server restart does not turn a shell into a
host process or leave a container intentionally running. Active snapshots and
snapshots pinned by running work remain available; cleanup must not remove
those references. Keep the server database and its toolenv data directory in
backups when recovery of workspace tools matters.

The SSH control channel for both bootstrap and harness login is the unified
`aether-workspace-shell` subsystem. `aether setup` is the login-mode CLI name,
not a separate transport or setup-only container path.
