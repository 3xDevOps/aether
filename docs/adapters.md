# Adding an agent or an adapter

Two independent seams. An **agent profile** teaches Aether how to launch an
agent CLI - that is all most agents need. An **adapter** additionally turns the
agent's machine-readable output into typed events. Adapters are optional by
design: a run without one still has a full PTY transcript and a git-diff
timeline, and no feature is allowed to hard-require adapter events.

Read [harnesses.md](harnesses.md) first for what already ships.

---

## Part 1: the agent profile

Everything lives in `internal/harness/harness.go`. Adding an agent is adding
one entry to the `profiles` map:

| Field | What to put there |
| --- | --- |
| `Name` | The value users pass to `--agent`. Match the map key. |
| `TUIArgs` | Argv for the interactive TUI. Use `TaskPlaceholder` (`{task}`) where the prompt goes. |
| `HeadlessArgs` | Argv for the machine-readable mode. Same placeholder. |
| `EnvPassthrough` | Environment variables copied from the server process into run containers when set. Only API keys and the base URLs that point the CLI at a model gateway. |
| `Env` | Fixed environment variables the CLI needs to start at all, applied after the workspace's own so a workspace cannot break the launch. Not for configuration - a variable belongs here only when the agent refuses to run without it. |
| `ACPMode` | The session mode a new Enhanced session starts in: the adapter's mode that acts without asking (`bypassPermissions` for Claude Code). Empty keeps the agent's default. |
| `PermissionEnv` | Variables that turn the agent's permission prompts off in every launch mode, for an agent with no flag for it. OpenCode's `OPENCODE_CONFIG_CONTENT` is merged key by key with the workspace's value. |
| `NoPermissionPrompt` | Set for an agent that never asks (pi), so it has no setting to apply. |
| `CredentialPaths` | Home-relative files or directories holding native login state, strictly below the home. They persist in the member home like everything else there. An account share mounts exactly these paths, read-write, from the owner's home into a recipient's run, so name the login file itself, not its configuration directory. |
| `InstallPaths` | Home-relative directories the CLI's `~/.local/bin` launcher links into, outside `~/.local/bin` and `~/.local/lib`; `claude` names `.local/share/claude`, where its native installer puts the versions it links to. A recipient's run that borrows the owner's installation mounts each read-only from the owner's home at the same path. Leave empty for an npm install (under `~/.local/lib`) or a single binary in `~/.local/bin`. |
| `UpdateScript` | Optional. The command that updates the CLI in `~/.local/bin` when a run launches ([harnesses.md](harnesses.md#updates-before-launch)). Make it a cheap no-op when the CLI is current, and update only the program. A launch can start the CLI while the update runs, so swap the new version in atomically. |
| `BorrowedState` | A home-relative JSON file and the keys a launch that borrows this login sets in the launcher's copy when they are absent, so the CLI starts signed in with the owner's login instead of running first-time setup against an empty home. `claude` sets `hasCompletedOnboarding: true` in `.claude.json`, because Claude Code's setup wizard shows its sign-in step regardless of an existing login. The file is created with mode 0600 when missing, rewritten in place when it is a JSON object, and left alone otherwise. |
| `LocalRoot` | Home-relative configuration root, the same path on the member's computer and in the member home. `aether config push` uploads from it, the **Files** editor edits it, and the explicit `profile` CLI commands read it. Empty means the agent has no configuration root. |
| `DenyNames` | Credential/token/keychain names, matched case-insensitively in every path component. `config.roots.credential_names` combines these with shared denials so `aether config push` leaves them out before reading them. The server and manual profile path enforce the same names; `*.pem`, `.env`, and `.env.*` are always denied. |
| `User` | An explicit numeric `uid:gid` for images whose configured user is a name. Usually leave empty. |
| `InstructionArgs`, `InstructionEnv` | How a non-ACP launch appends the run's [agent instructions](coordination.md#agent-instructions) to the agent's system prompt. `InstructionArgs` takes `{instructions}` (the raw text) or `{instructions_quoted}` (a JSON-quoted string, for Codex's `-c developer_instructions=`). `InstructionEnv` is for an agent with no flag for it and points at `/run/aether/instructions.md`, which the server writes read-only. Nothing is written to the member home or repository. Without either, the instructions follow the task prompt. |

Rules that are easy to get wrong:

- **Turn permission prompts off in every mode.** Put the agent's flag in
  `TUIArgs`, `HeadlessArgs`, `ResumeArgs` and a native `ACPArgs`, set
  `ACPMode` for an adapter, or use `PermissionEnv`. Auto-permission is the
  default stance: the container is the isolation boundary, and an agent
  stopping to ask for approval in a headless fleet is a hang, not a
  safeguard. `TestEveryShippedAgentRunsWithoutPermissionPrompts` fails until
  the new agent's setting is listed in it.
- **`LocalRoot` names native configuration, not login storage.** OpenCode uses
  `.config/opencode` for configuration and `.local/share/opencode/auth.json`
  for account sharing. Registry roots are home-relative defaults, not
  environment-expanded paths; native configuration overrides do not relocate
  `aether config` or manual profile commands.
- **`CredentialPaths` and `DenyNames` are two different lists.** The first says
  what an account share *mounts* into another member's run; the second says
  what `aether config push` and explicit profile commands must never
  *upload*. A credential file usually appears in both, from opposite
  directions.
- **Nothing under `LocalRoot` may be assumed private.** `aether config push`
  uploads every named file that passes the credential, runtime, and
  secret-scanner checks. The scanner finds known secret patterns, not every
  secret.
- **Configuration changes the shared member HOME.** `aether config push` and
  the **Files** editor write the authenticated member's persistent home,
  visible to that member's active and future runs. No daemon watches
  `LocalRoot` or automatically synchronizes configuration.
- **Directory size does not truncate uploads.** `aether config push` sends the
  named files in bounded requests. Runtime and credential policy applies
  before the 64 MiB per-file size check. Every omission is reported; a larger
  file can be installed or downloaded in the persistent home through
  `aether terminal`. A failed request reports confirmed and unknown outcomes
  and is not retried automatically.
- **File metadata is limited.** New files get the local permission bits
  (`0644` from Windows); overwrites retain existing remote modes. Symlinks
  are not uploaded.
- **Profile snapshots are optional history, not the HOME.** `aether config push`
  and Files edits do not create CLI snapshots. A run's snapshot pin records
  provenance, not an isolated writable copy. Manual profile push and rollback
  overlay snapshot files into the same shared HOME; absent paths are not
  deleted, so rollback is not an exact-tree restore.
- **Instructions go in the system prompt.** Use the vendor's documented
  mechanism for appending to it, never one that replaces the built-in
  prompt, so the instructions survive compaction; `aether-internal skill`
  fetches the assignment-specific details from the run socket.

- **An argv override must stay verbatim.** The scheduler drops the shipped
  instruction and status wiring when a definition replaces a shipped
  agent, because nothing checks that the override is still that CLI.

Then add coverage in `internal/harness/harness_test.go` alongside the existing
table-driven cases, and a row in the tables in
[harnesses.md](harnesses.md) - a change that makes a doc wrong is not finished
until the doc is fixed.

That is the whole agent change. The scheduler resolves argv, mounts, run user
and per-launch instruction/status assets from the profile; nothing else needs
editing.

---

## Part 2: the adapter

Only worth writing when the agent has a **structured output mode** - a
newline-delimited machine-readable stream, like Claude Code's
`--output-format stream-json`. Adapters only ever see headless runs.

The interface, from `internal/adapter/adapter.go`, is one method:

```go
type Adapter interface {
    ConsumeLine(line string) []events.Payload
}
```

Register a constructor in the `adapters` map keyed by agent name, and the
`Manager` does the rest: it watches the bus for headless runs entering
`running`, taps the run's PTY output, normalizes it into lines, feeds them to
your adapter, and publishes whatever payloads come back under the run's
workspace and run IDs.

### The four rules

1. **Never return an error.** There is no error in the signature and that is
   deliberate. A line that does not parse is not a failure - it is ordinary PTY
   output that happened to flow past. Return `nil` and move on.
2. **Expect terminal noise.** Headless runs still go through a TTY, so output
   arrives with CRLF endings, interleaved escape sequences, and arbitrary chunk
   boundaries. `LineNormalizer` scrubs that before your adapter sees it, but do
   not assume a line is JSON: check the first byte before unmarshalling, the
   way `internal/adapter/claude.go` does.
3. **Stay stateless if you can.** A fresh adapter is constructed per run. If
   you must correlate records - a tool result to its call - carry the agent's
   own IDs on the payload (`ToolUseID`) instead of keeping a map.
4. **Summarize, do not transcribe.** `Detail` is a short human-readable line
   for the timeline, truncated by the adapter (the Claude adapter caps it at
   256 bytes). The full output already lives in the PTY transcript.

### What to emit

Most adapter output is `events.AgentEventPayload`:

| `Kind` | Meaning | Fields that matter |
| --- | --- | --- |
| `AgentToolCall` | The agent invoked a tool | `Tool`, `ToolUseID`, `Detail` |
| `AgentToolResult` | A tool invocation finished | `ToolUseID`, `IsError` |
| `AgentSubagent` | The agent spawned a subagent | `Tool`, `ToolUseID`, `Detail` |
| `AgentSession` | The agent's own session ID, surfaced on the timeline | `HarnessSessionID` |

Token usage is **not** an agent event: report it as `events.RunCostPayload` so
it reaches the cost rollups and workspace budgets. An agent that reports no
usage leaves its runs marked unmetered, which `aether cost` and `aether budget`
both say out loud.

`AgentSession` carries the agent's own conversation ID, which is unrelated
to any Aether scope. It is a timeline record only: Aether does not assign a
conversation ID from it or use it to control reopening a run. Emit it anyway - it is
what an operator needs to find the conversation in the agent's own tooling.

### Testing

Record a real stream and replay it. `internal/adapter/testdata/` holds two
fixtures per agent-shaped case, and both matter:

- `claude_clean.jsonl` - the raw stream as the agent documents it.
- `claude_tty.jsonl` - the same stream after a TTY has had its way with it.

Assert on the payloads your adapter produces from each. A fixture-driven test
is the whole test: no live agent, no network, no mocking of the bus.

---

## Where the seam ends

The registry is a map and adapters are one file each, on purpose. If a change
needs a plugin loader, a config file, or a new interface, it is probably not an
adapter change. Keep broader changes aligned with the public architecture and
protocol documentation before building them.
