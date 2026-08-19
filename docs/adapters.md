# Adding a harness or an adapter

Two independent seams. A **harness profile** teaches Aether how to launch an
agent CLI - that is all most agents need. An **adapter** additionally turns the
agent's machine-readable output into typed events. Adapters are optional by
design: a run without one still has a full PTY transcript and a git-diff
timeline, and no feature is allowed to hard-require adapter events.

Read [harnesses.md](harnesses.md) first for what already ships.

---

## Part 1: the harness profile

Everything lives in `internal/harness/harness.go`. Adding an agent is adding
one entry to the `profiles` map:

| Field | What to put there |
| --- | --- |
| `Name` | The value users pass to `--agent`. Match the map key. |
| `TUIArgs` | Argv for the interactive TUI. Use `TaskPlaceholder` (`{task}`) where the prompt goes. |
| `HeadlessArgs` | Argv for the machine-readable mode. Same placeholder. |
| `EnvPassthrough` | Environment variables copied from the server process into run containers when set. API keys only. |
| `CredentialPaths` | Home-relative directories holding native login state. Persisted per member and mounted read-write into every run. Directories, not files. |
| `LocalRoot` | Home-relative directory captured by profile sync. Empty means the harness has no profile sync. |
| `DenyNames` | Basenames profile sync always excludes - credential files, token caches, keychains. |
| `User` | An explicit numeric `uid:gid` for images whose configured user is a name. Usually leave empty. |
| `MCPConfigFlag` | The CLI's flag for a server-supplied MCP server config, if it has one. Set it and the run is wired to the coordination bridge; leave it empty and coordination degrades to the overlap notice. |
| `ResumeFlag` | The CLI's flag for continuing the conversation it last had in the working directory (Claude Code's `--continue`). Set it and relaunching an interrupted run asks the agent to carry on; leave it empty and the relaunch starts fresh. See [failure-handling.md](failure-handling.md). |

Rules that are easy to get wrong:

- **Apply the agent's full-permission flag in both modes.** Auto-permission is
  the default stance: the container is the isolation boundary, and an agent
  stopping to ask for approval in a headless fleet is a hang, not a safeguard.
- **`CredentialPaths` and `DenyNames` are two different lists.** The first says
  what to *persist* across runs; the second says what profile sync must never
  *upload*. A credential file usually appears in both, from opposite directions.
- **Nothing under `LocalRoot` may be a secret.** Everything there is uploaded
  from the member's laptop. If the harness mixes config and tokens in one
  directory, the token filenames belong in `DenyNames`.
- **The MCP and resume flags belong to the CLI they ship with.** A deployment
  that overrides a harness's argv gets neither appended, because nothing checks
  that the override is still that CLI.

Then add coverage in `internal/harness/harness_test.go` alongside the existing
table-driven cases, and a row in the tables in
[harnesses.md](harnesses.md) - a change that makes a doc wrong is not finished
until the doc is fixed.

That is the whole harness change. The scheduler resolves argv, mounts, run user
and MCP config from the profile; nothing else needs editing.

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

Register a constructor in the `adapters` map keyed by harness name, and the
`Manager` does the rest: it watches the bus for headless runs entering
`running`, taps the run's PTY output, normalizes it into lines, feeds them to
your adapter, and publishes whatever payloads come back under the run's session
and run IDs.

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
   you must correlate records - a tool result to its call - carry the harness's
   own IDs on the payload (`ToolUseID`) instead of keeping a map.
4. **Summarize, do not transcribe.** `Detail` is a short human-readable line
   for the timeline, truncated by the adapter (the Claude adapter caps it at
   256 bytes). The full output already lives in the PTY recording.

### What to emit

Most adapter output is `events.AgentEventPayload`:

| `Kind` | Meaning | Fields that matter |
| --- | --- | --- |
| `AgentToolCall` | The agent invoked a tool | `Tool`, `ToolUseID`, `Detail` |
| `AgentToolResult` | A tool invocation finished | `ToolUseID`, `IsError`, `Detail` |
| `AgentSubagent` | The agent spawned a subagent | `Tool`, `ToolUseID`, `Detail` |
| `AgentPause` | The agent is waiting on plan review or approval | `Detail` |
| `AgentSession` | The harness's own session ID, surfaced on the timeline | `HarnessSessionID` |

Token usage is **not** an agent event: report it as `events.RunCostPayload` so
it reaches the cost rollups and session budgets. A harness that reports no
usage leaves its runs marked unmetered, which `aether cost` and `aether budget`
both say out loud.

`AgentSession` is a timeline record, not the resume mechanism. Relaunching an
interrupted run uses the profile's `ResumeFlag`, which names no session - see
[failure-handling.md](failure-handling.md). Nothing reads `HarnessSessionID`
back today; emit it because it is what an operator needs to find the
conversation in the harness's own tooling.

### Testing

Record a real stream and replay it. `internal/adapter/testdata/` holds two
fixtures per harness-shaped case, and both matter:

- `claude_clean.jsonl` - the raw stream as the harness documents it.
- `claude_tty.jsonl` - the same stream after a TTY has had its way with it.

Assert on the payloads your adapter produces from each. A fixture-driven test
is the whole test: no live agent, no network, no mocking of the bus.

---

## Where the seam ends

The registry is a map and adapters are one file each, on purpose. If a change
needs a plugin loader, a config file, or a new interface, it is probably not an
adapter change. Keep broader changes aligned with the public architecture and
protocol documentation before building them.
