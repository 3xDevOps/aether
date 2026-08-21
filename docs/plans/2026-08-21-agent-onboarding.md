# One-Step Agent Onboarding Implementation Plan

**Goal:** Replace the three-step agent onboarding (admin harness JSON, then
`workspace bootstrap`, then `setup`) with one member-facing command:
`aether agent add <name> --workspace <ws>`.

**Architecture:** Harness definitions become member-scoped rows in SQLite,
registered over the existing control channel; no server restart, no env-var
JSON. A new workspace-shell mode combines tool bootstrap and vendor login in
one container session and discovers credential paths by observing what the
login wrote into the (now host-backed) container home. Existing mechanisms -
tool staging/promotion, credential-home mounts, definition validation - are
reused, not duplicated.

**Non-goals:**

- No interactive fallback inside `aether run`; an unknown agent fails with a
  message naming the exact `agent add` command. Runs can be headless; one
  interactive flow is enough to maintain.
- No recipe registry beyond the shipped profiles. `agent add claude` uses the
  shipped profile as its defaults; a curated table for third-party agents can
  come later if wanted.
- No workspace-scoped or admin-editable definitions over RPC. Member rows
  only. The `--harness-definitions` env path stays as the admin override and
  still wins.
- `workspace bootstrap` and `setup` remain as repair commands (re-login
  without reinstall, snapshot rollback) but stop being the documented
  onboarding path.

## Design

### Definition resolution precedence

When a run or shell resolves a harness name for a member:

1. Server-wide spec from `--harness-definitions` (admin pin, unchanged).
2. The member's own stored definition.
3. The shipped registry (claude, codex, aider, opencode, custom, fake).

Member definitions carry the same fields as the existing generic definition
and pass the same validation (`harness.Definition.Validate`). They produce
profiles with no MCP or resume flags, exactly like admin definitions today.
Security note: a member definition only shapes argv inside that member's own
container, which the member already controls via bootstrap, so this grants no
new capability.

### The agent-setup shell

A third workspace-shell mode alongside bootstrap and login. Differences from
bootstrap:

- In addition to the tool staging mount at `~/.local`, the member's
  per-harness credential home directory (`<homes>/<member>/<harness>/`) is
  bind-mounted read-write at the container `$HOME`. Whatever the vendor's
  login flow writes to home is thereby persisted in exactly the place future
  runs' credential mounts read from. Mount order puts the home mount before
  the staging mount so the more specific path nests over it.
- On clean exit, the server snapshots `~/.local` staging into a tool snapshot
  (existing promotion, including executable verification), then diffs the
  host-side home directory: top-level entries that now exist, minus a small
  noise list (`.local`, `.cache`, shell history files), become the
  definition's credential paths. If exactly one entry was created it also
  becomes the profile root; otherwise the profile root stays empty.
- For a custom name, the server validates and stores the member definition
  assembled from the request's argv templates plus the discovered credential
  paths. For a shipped name, no definition is stored; the shell only combines
  bootstrap and login.
- A banner tells the member what to do, mirroring the bootstrap banner. The
  login mode gets the same courtesy banner while we are here (it currently
  prints nothing).

Credential-path discovery converts host-relative entries to absolute
container paths using the existing home-dir resolution for the run user, and
discovered paths that fail container-path validation (outside `/root` or
`/home/aether`, device paths) abort registration with a clear error rather
than storing a lame definition.

### CLI

New top-level `agent` command:

- `aether agent add <name> [--workspace <ws>] [--tui <argv>] [--headless <argv>]`
  - Shipped name: no prompts, no definition; opens the agent-setup shell.
  - Custom name: argv templates from flags, or prompted with defaults
    (`<name> {task}` / `<name> -p {task}`); Enter accepts. Prompting happens
    before the shell opens so the request carries the full proposal.
  - After the shell exits cleanly the server has registered everything; the
    command prints the summary the server already wrote into the stream.
- `aether agent list`: shipped harness names plus the member's registered
  definitions, marked accordingly.

`aether run`/launch with an unknown agent returns an error that names
`aether agent add <name>` - the scheduler error today is a bare
`unknown harness`.

### Wire additions

- `agent.register`: member-scoped upsert of a definition; server validates.
- `agent.list`: shipped names plus the caller's definitions.
- Workspace-shell request gains the agent-setup mode and a definition
  proposal (argv templates; executable defaults to the agent name).

### Storage

New table keyed `(member_id, name)` holding the definition as a JSON blob
plus timestamps, appended as the next schema migration. JSON keeps the schema
stable while definition fields evolve; validation happens in code on both
write and read. Deleting a member cascades.

## Tasks

Each task: test first, minimal implementation, run the package tests, commit.

### Task 1: store - member harness definitions

- Modify: `internal/store/migrate.go` (append migration), `internal/store/store.go`
  (interface + row type), `internal/store/sqlite.go` (implementation).
- Test: `internal/store/store_test.go` - upsert then get round-trips fields;
  get for a missing row returns the store's not-found error; list returns
  only the member's rows; upsert overwrites.
- Produces: `UpsertHarnessDefinition`, `GetHarnessDefinition`,
  `ListHarnessDefinitions` on the store interface, operating on a row type
  wrapping `harness.Definition`.

### Task 2: scheduler - member-aware resolution

- Modify: `internal/scheduler/scheduler.go` - `command` gains the member ID,
  consults the store between the server-wide spec and the shipped registry,
  and the unknown-harness error names `aether agent add`. Callers
  (`Launch`, `WorkspaceShell`) pass the member they already hold.
- Test: `internal/scheduler/scheduler_test.go` - a stored member definition
  resolves for its owner and not for another member; the server-wide spec
  still wins over a member definition; unknown harness error mentions
  `agent add`.

### Task 3: protocol + sshd - agent.register / agent.list

- Modify: `internal/protocol/protocol.go` (method names),
  `internal/protocol/wire.go` (params/results),
  new `internal/sshd/agent.go` (handlers, registered like tools.go).
- Test: `internal/sshd` handler tests following the tools tests: register
  validates via the harness validator and rejects bad argv; list merges
  shipped names with stored rows.

### Task 4: agent-setup workspace-shell mode

- Modify: `internal/domain` (new shell mode + request validation),
  `internal/protocol/wire.go` (mode constant, definition proposal fields),
  `internal/scheduler/workspace_shell.go` (home mount, banner, exit-time
  discovery + registration), `internal/scheduler/environment.go` only if the
  bootstrap purpose needs the extra mount hook.
- Test: `internal/scheduler/workspace_shell_test.go` - agent-setup shell with
  the fake runtime: clean exit promotes tools, persists what login wrote,
  registers a definition whose credential paths match the written entries;
  noise entries are excluded; dirty exit registers nothing; shipped name
  stores no definition.

### Task 5: CLI - agent command and run hint

- Create: `cmd/aether/agent.go` (+ test for prompt defaulting and list
  output, following existing command tests).
- Modify: `cmd/aether/register.go` if needed; quickstart error text asserted
  in scheduler test already covers the run path.

### Task 6: E2E verification

- Scratch server (dev binary, throwaway data dir, port 22222), fake or real
  Docker runtime: `aether agent add myagent` end to end with a stub
  executable standing in for a vendor CLI; then `aether run` with the
  registered agent resolves argv. This is the check that the pieces mesh;
  it happens before review, not as a committed integration test unless the
  existing integration suite has a natural slot.

### Task 7: documentation

- Modify: `docs/quickstart.md` (agent section collapses to `agent add`),
  `docs/bootstrap.md` and `docs/harnesses.md` (bootstrap/setup repositioned
  as plumbing; member definitions explained; env-var stays admin override),
  `docs/cli.md` equivalent if present.

## Verification gates

`make fmt-check && make vet && make test && make public-audit`, the E2E pass
from Task 6, then a Codex adversarial review round; genuine findings fixed
before the PR opens.
