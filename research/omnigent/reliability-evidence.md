# Harness and coordination reliability: Omnigent vs Aether, from bug history

Sources: Omnigent `8b46a8a37961616e3e78d7f96c37ed8ccf08f07e` (2026-10-24),
Aether `77b4f3bb` (2026-10-23), GitHub issues read on 2026-10-25.
Citations use `omnigent:<path>:<line>`, `aether:<path>:<line>`, short commit
SHAs and `#n` for issues or PRs in the project named. "(inference)" marks a
conclusion that was not read directly.

## Verdict

1. Neither integration layer has stabilized. Omnigent: 2,439 fix PRs in 134 days, 1,773 bug issues
   (443 open), harness-scope fixes rising monthly (127 in June, 246 in October). Aether: 173 fix PRs
   (1,314 fix headers with bundled sub-fixes) in 68 days, also rising (71 in September, 98 in October).
2. Both fail in the same classes in similar proportions (2-8% of fix headers each), so these
   failures come from driving vendor CLIs, not from either codebase.
3. Omnigent is not empirically more consistent. Its Claude, Cursor and Hermes paths paste into a
   tmux TUI and read the screen back. By hand count, 58 of its 336 native fixes (49 of them Claude)
   come from that screen-driving, rising monthly (2, 7, 9, 15, 25). Claude also leads its sampled
   input-loss (16/36) and approval (13/26) issues; 17 fixes name the release that broke it.
4. Protocol paths are the robust design in both: Omnigent's codex app-server and `opencode serve`,
   Aether's ACP (Enhanced). Do not replace Aether's ACP path with Omnigent's tmux bridge.
5. Omnigent's real lead is knowing when it fails: delivery acks and tracing IDs, a resilience lab
   with real TUIs, a mock model and fault proxies, a drift bench, pinned CLIs in CI, nightly e2e and
   a triage bot. Aether learns of failures when a person notices one.
6. Aether's flakiness has three sources. (a) Coordination: swarm, mission and mail fixes are 7.8% of
   its fix headers (Omnigent 1.1%), and 8 swarm-audit issues are open. (b) Standard mode: a steer is
   a blind stdin write, and status comes from hooks or silence. (c) Bundled fixes: median 10 files
   per fix PR (Omnigent 3).
7. Aether already removes two classes by design: tool approvals are off in every mode (#286), and
   one container per run contains process leaks (Omnigent has 92 orphan or leak fix headers, Aether 0).
8. Eliminate in Aether: route Standard-run messages through the mailbox and native wake; default
   swarm runs to Enhanced; make the status reporter durable; gate CLI auto-updates on a CI-verified
   version.
9. Replace Aether's acpmock-only Enhanced tests with Omnigent-style real pinned adapters and CLIs
   driven by a mock model under fault proxies (`omnigent:tests/e2e/resilience/`, Apache-2.0).

## 1. Data and normalization

| Measure | Omnigent | Aether |
| --- | --- | --- |
| Window | 2026-06-12 to 2026-10-24 (134 days) | 2026-08-18 to 2026-10-23 (68 days); history before the public release is squashed into `7eb3ee1a` |
| Commits on main | 5,381 (PRs opened: 8,872; issues: 2,583) | 507 |
| Commit authors | 307 | 8 |
| Source lines | ~554k Python + ~403k TS; ~1.16M Python test lines | ~191k Go + ~140k TS; ~207k Go test lines |
| `fix` PRs (top-level) | 2,439 (45% of commits) | 173 (34%) |
| Fix PRs per day | 18.2 | 2.5 |
| Distinct fix headers, top-level plus `* fix(...)` sub-commits in squash bodies | 6,970 | 1,314 |
| Fix headers per 10k source lines | 73 | 40 |
| Median / p90 files per fix PR | 3 / 10 | 10 / 35 |
| Reverts | 34 | 1 |
| Fix bodies citing a regression or an earlier PR that broke it | 334 (13.7%) | 9 (5.2%) |
| Bug issues | 1,773 labelled `Bug`, 443 open; filing rose from about 12 a day in August to about 21 a day in October | 9 issues on GitHub, 8 open, all from the 2026-09-25 swarm audit (#225-#231, #236) |
| Harness-scope fix PRs per month | Jun 127, Jul 150, Aug 162, Sep 246, Oct 246 | Aug 2, Sep 26, Oct 33 (integration packages) |
| Fix share of commits per month | Jun 35%, Oct 49% | Sep 32%, Oct 38% |

Normalization caveats:

- Omnigent has about 38x Aether's contributors and about 3x its code, and is
  used by far more people. Raw counts measure activity, not defect density.
- Aether tracks almost no bugs in issues, so it has no measurable open-defect
  backlog. Omnigent's triage bot labels 69% of all issues `Bug`, including
  design gaps.
- Aether squash-merges many fixes per PR. Omnigent squashes one fix per PR.
  Counting sub-commit headers corrects most of the difference.
- The commit sets were selected as follows:
  - Omnigent "harness scope" is the `fix(<scope>)` subjects for `*-native`,
    `runner`, `host`, `harness(es)`, `terminal(s)`, `sessions`, `sandbox`,
    `acp`, `claude-sdk`, `codex`, `pi`, `cursor`, `opencode`, `hermes`,
    `antigravity`, `kiro`, `openai-agents`, `subagent(s)`, `runtime`,
    `tunnel`, `routing`, `claude`, `agents`, `repl`, `tools` and `inner`
    (931 PRs).
  - Aether "integration" is every fix commit touching `internal/{harness,acphost,adapter,ptyhost,coord*,mission,integration,mcpbridge,agentstatus,approvals,scheduler}`
    or `web/src/lib/{status,needs-you,reasons,acp-stream,send-to-agent,permissions,events,stream}.ts`
    (61 PRs).

## 2. Failure classes

Columns:

- **Fix-header share:** the share of distinct fix headers matched by a
  priority-ordered keyword classifier (`/tmp/relclass/classify.py` during this
  run; rules are listed in section 6). Spot checks put precision at about
  70-80% per class.
- **Omnigent issue share:** a hand-reviewed sample of 600 bug issues, the
  latest 100 of each of six date windows.

| Class | Omnigent fix headers | Omnigent issues (n=600) | Aether fix headers |
| --- | --- | --- | --- |
| Input delivery lost | 315 (4.5%) | 36 (6.0%), Claude 16 | 67 (5.1%) |
| Wrong or stuck status | 316 (4.5%) | 24 (4.0%) | 83 (6.3%) |
| Approval or elicitation not surfaced | 223 (3.2%) | 26 (4.3%), Claude 13 | 25 (1.9%) |
| Resume or reattach | 401 (5.8%) | 25 (4.2%) | 102 (7.8%) |
| Auth expiry or login | 422 (6.1%) | 48 (8.0%) | 36 (2.7%) |
| Transcript or event loss | 382 (5.5%) | 26 (4.3%) | 86 (6.5%) |
| Sub-agent or handoff | 429 (6.2%) | 32 (5.3%) | 30 (2.3%) |
| Inter-agent message delivery | 74 (1.1%) | 12 (2.0%) | 103 (7.8%) |
| Harness drift and TUI surfaces | 278 (4.0%) | 8 strict (1.3%), plus TUI-coupled issues filed under input or approval | 57 (4.3%), mostly Aether's own dashboard surfaces |
| Sandbox, container, process lifecycle | 371 (5.3%) | 38 (6.3%) | 44 (3.3%) |
| Network, tunnel | 297 (4.3%) | 28 (4.7%) | 65 (4.9%) |
| Terminal and PTY rendering | 174 (2.5%) | (UI-only: 62) | 39 (3.0%) |
| Other (UI, config, packaging, CI) | 3,288 (47%) | 222 (37%) | 577 (44%) |

### 2.0 Hand classification of the integration fixes

A second, hand-made pass labelled each fix with one class and one root-cause
mechanism. It read the subject, plus the body where the subject was ambiguous.

- **Omnigent:** all 336 `fix(native)`, `fix(claude-native)`,
  `fix(codex-native)`, `fix(pi-native)`, `fix(cursor-native)` and
  `fix(opencode-native)` commits.
- **Aether:** 152 integration fixes: 18 top-level commits plus 134 squash-body
  sub-fixes, selected from integration-path fixes and a `--grep` for inject,
  steer, deliver, inbox, wake, hook, status, stalled, needs you, reattach,
  restore, resume, acphost and adapter.

| Mechanism | Omnigent (n=336) | Aether (n=152) |
| --- | --- | --- |
| Reading the screen or typing keystrokes into a TUI (Omnigent tmux; Aether PTY injection) | 58 (17.3%); 49 of them claude-native, 35% of its 141 fixes; codex-native 3 of 91 | 8 (5.3%), all in September |
| Status or liveness taken from PTY output bytes (Aether only) | - | 6 (3.9%) |
| Vendor hooks or status files | 28 | 23 |
| Tailing vendor transcripts or stores | 42 | - (Aether records its own streams) |
| Protocol (app-server, SDK, ACP) | 45 | 40, all in October after Enhanced shipped |
| The project's own server, runner, store or concurrency | 151 (58 of them model and provider routing) | 67 |
| Vendor CLI behavior | 11 | 7 |

| Class (hand) | Omnigent | Aether |
| --- | --- | --- |
| Input delivery | 43; 32 from TUI observation | 35; 7 from PTY injection, 9 ACP, 14 Aether infrastructure |
| Status | 22 | 42 |
| Approval or elicitation | 14 | 12 |
| Resume or reattach | 37 | 13 |
| Transcript or event | 28 | 8 |
| Sub-agent | 15 | 7 |
| Harness drift (version cited) | 9 | 1 |
| Startup dialog blocking the session | 15; 10 answered by keystroke | 2 |
| Process lifecycle | 12 | 2 |

Omnigent's TUI-observation fixes per month were 2, 7, 9, 15 and 25 (October
counted to the 24th). Their share held at 16-22% from July on, while total
commits per month stayed flat at 979 to 1,197, so the path is not converging.
Codex, observed over JSON-RPC, has 3 TUI fixes in 91; its drift arrives as
typed protocol errors (`a77807e56`, `4d963a360`, `12b0a71ba`), not misread
screens.

Startup dialogs are their own class in Omnigent:

- Pi's "Trust project folder?" (`d9eb6458f`).
- Codex "Hooks need review" and its project-trust screen (`0e0bc901b`,
  `6ac341819`).
- The Claude bypass-permissions consent (`cbd800feb`).
- Claude's MCP approval dialog (`c7ed0f987`), which "fires no hook".
- "Teach auto mode about your environment?" (`1a6a2d6b4`).
- "Make auto mode your default permission mode?" (`2be4c6f63`, 2.1.308).

Aether had two: Claude's first-time setup for a borrowed login (`b01816e1`)
and Gemini CLI 0.64.0's folder-trust check (`b946c95e`).

Scratch labels: `/tmp/relclass/hand/omni_labels.txt.joined.tsv` and
`/tmp/relclass/hand/aether_units.tsv` (during this run).

### 2.1 Input delivery lost

**Omnigent:**

- **How claude-native delivers.** It writes a bracketed paste with tmux
  `load-buffer` and `paste-buffer -p`, then sends Enter. Two gates come first:
  the `❯` input box must have rendered and `tmux.json` must exist. It then
  polls `capture-pane` until the draft leaves the box, re-pressing Enter for
  up to 20 s (`omnigent:omnigent/harnesses/claude_native/bridge.py:4185-4604`).
- **Fixes**, each a surface the paste can land on:
  - `a9e763456` (#6297): require a delivery acknowledgement.
  - `325a260fa` (#6584): first prompt dropped while the terminal boots.
  - `b8687ebe4` (#5283): composer already occupied.
  - `53b821616` (#9936): input box held in shell mode, where `!` runs the text as bash.
  - `4fc24cdb2` (#10671): a pending Claude prompt.
  - `73c4b90ce` (#10122): input box lost focus.
  - `501e3fd13` (#6587): Codex slash popup swallows Enter.
  - `cfffb8bf0` (#7150): TUI transiently unresponsive.
  - `983e90725` (#11101): unreadable captures.
  - `20f2c4da6` (#8997): drafts rendered below the prompt row.
- **Issues:**
  - #8208: message left in the input box while the turn reports delivered.
  - #10970 (open): Cursor first message lost.
  - #10929 (open): Qwen drops a boot-time message.
  - #10471 (open): `sys_session_send` falsely reports "injected".
- **Limits of its own diagnostics.** The delivery confirmation only proves
  `draft_cleared`. The tracing doc admits that matching is text-based and the
  queue is in memory
  (`omnigent:docs/NATIVE_INPUT_DELIVERY_DIAGNOSTICS.md:47`, `:144-147`).
- **Protocol paths.** codex-native sends `turn/start` and `turn/steer` with
  `expectedTurnId` and gets an explicit outcome per call
  (`omnigent:omnigent/inner/codex_native_executor.py:389-437`).

**Aether:**

- **Mail:** a durable at-least-once mailbox with ack tokens. It is never
  typed into a terminal: "Do not write to the agent's terminal. Aether has no
  terminal-input fallback" (`aether:docs/harness-integration.md:101-104`).
- **Enhanced:** `session/prompt`. A prompt counts as sent on the agent's first
  update, or after a 1.5 s grace with no refusal
  (`aether:internal/acphost/session.go:139-142`). Fixes:
  - `97d2cc46` (#490): a cancel right after a prompt was lost. The race
    failed 1 in 170 runs on main.
  - `de35d8bb`: a queued prompt lost when the host closes the agent.
  - After a restart, queued messages read **Not sent** and are not resent
    (`aether:docs/enhanced-runs.md:215-224`).
- **Standard steer** (a person's message, `aether message`, the connector) is
  one write of the message plus `\r` to the PTY. It has no readiness gate, no
  bracketed paste and no submit check
  (`aether:internal/scheduler/driver.go:51-52`,
  `aether:internal/ptyhost/session.go:927-958`).
- **What gets recorded as sent.** Collab records `ReceiptSent` once the
  write returns no error (`aether:internal/collab/delivery.go:138`). The ACP
  driver, by contrast, settles delivery from the agent's own answer
  (`aether:internal/scheduler/acp_driver.go:253`).
- **Known failures on this path:**
  - `40dd88f5`: OpenCode needs a second Enter, so steers "never reached the
    conversation". The fix is a static per-harness suffix
    (`aether:internal/harness/harness.go:325-327`).
  - `2d37db09`: the terminal echoes the injected line "even when the agent
    never reads its input", so steering a hung agent cleared its stall.
  - `d67aab00` (#147): pastes sent as bare CRs.
  - `3c025860` (#246) moved automated notices off this path, after
    `f2c7f904` found them typed into a login shell and run as globs.
- (inference) The low fix count reflects that nothing verifies delivery, not
  that delivery works. Every surface in Omnigent's list above (shell mode,
  history search, an occupied composer, a boot not yet finished) is a state a
  blind write into the same Claude Code TUI can hit, and each would still
  record "Sent". Exposure is lower than Omnigent's: a member steering a
  Standard run is usually watching it, and the dashboard has defaulted new
  agents to Enhanced since `ec107c02` (#499, 2026-10-21).
- **Root cause in both:** typing into a TUI has no delivery contract.
- **Eliminated by design** where input goes through a protocol (ACP,
  app-server, `opencode serve`) or a durable mailbox. Fixed instance by
  instance on every tmux path.
- **For Aether:** send `aether message` to Standard runs through the mailbox
  and native idle wake (Claude `asyncRewake`, pi/omp/OpenCode) wherever one
  exists. Where raw typing remains, bracket the paste when the agent has
  enabled mode 2004, which ptyhost already tracks
  (`aether:internal/ptyhost/mode.go:21`). Report the write as "typed", never
  as "delivered".

### 2.2 Wrong or stuck status

**Omnigent:**

- **claude-native status** layers four sources:
  - OSC 7501 terminal reports.
  - Claude Code's undocumented `sessions/<pid>.json`, present only since
    v2.1.139 (`omnigent:omnigent/harnesses/claude_native/status_file.py:1-23`).
  - A pane-diff idle detector.
  - Hooks.
- **Fixes:**
  - `4d3055f21` (#4344): the status file became the source of truth.
  - `cf34d7b90` (#4132): Claude ≥2.1.197 writes a new `shell` status.
  - `6a6bcf1f8` (#4195): the working indicator across turns.
  - `7d9dd710d` (#2082): busy after `/model`.
- **Open issues:**
  - #11418: status stays `running` after the turn.
  - #11484: stuck running when the StopFailure hook is lost.
  - #11284: an orchestrator running forever.
  - #2702: `capture-pane` at 5 Hz per terminal dominates runner CPU.
  - R8: Codex sticks on running after a reconnect, because the app-server
    stream has no cursor (`omnigent:docs/network-resilience.md:146-156`).

**Aether:**

- **Enhanced:** turn end is the `session/prompt` response. ACP still missed
  harness behavior: Claude Code returns `end_turn` while background work
  continues, so runs read idle mid-work (`de558930`, #363, 12 sub-fixes).
  Related fixes: `15edec58` (#424, interrupted turns), `147150ce` (#353,
  mislabeled after reboot), `01afde54` (#325, status age), `3530b696` (#267,
  run not finished on report).
- **Standard:**
  - Status comes from vendor hooks through a reporter that "never fails",
    with no retry or spool (`aether:cmd/aether-server/report.go:42-46`).
  - Codex reports only at turn end.
  - Copilot, Gemini, Cursor and Antigravity have no status at all and are
    judged on silence, with reason `stalled:`
    (`aether:docs/harnesses.md:42-44`, `:515-525`).
- **Aether-specific:** the run outcome depends on the agent calling
  `aether-internal report`. A turn without one parks as `waiting` ("turn
  ended without a report", `aether:docs/failure-handling.md:57`). (inference)
  That ties status to the model following instructions, a dependency
  Omnigent does not have.
- **Root cause:** status inferred from side channels: screen, private files,
  fire-and-forget hooks, silence.
- **Reduced by design** with protocol status (both projects' ACP and
  app-server paths). Neither removes vendor-specific turn semantics.
- **For Aether:**
  - Spool reporter events and retry them.
  - Run agents that have no Standard reporter in Enhanced by default for
    unattended work. Setup already suggests Enhanced for them
    (`aether:docs/harnesses.md:36-38`).

### 2.3 Approval or elicitation not surfaced

**Omnigent:**

- **Mechanism.** It surfaces tool approvals by policy. A `PermissionRequest`
  hook long-polls the server and falls back to the terminal prompt after 8
  hard failures (`omnigent:omnigent/harnesses/claude_native/hook.py:127-136`,
  `:927-990`).
- **Fixes:**
  - `f9952406f` (#6964): the 5-minute request cap.
  - `ba62bbc14` (#10564): network outage.
  - `804e349ae` (#10730): a lapsed login.
  - `29bf5d844` (#7239): AskUserQuestion surfaced twice.
  - `3c867c34a` (#5923): plan approval sent by keystroke.
- **Issues:**
  - #11093 (open): AskUserQuestion stranded in the terminal picker.
  - #8107 (open): OpenCode permission prompt only in the terminal.
  - #8101: 16 of 18 agy interaction kinds dropped.
  - Resilience findings R1 and R2.

**Aether:**

- **Mechanism.** Every agent runs without permission prompts by default in
  every mode (`7b698367`, #286; `aether:docs/adapters.md`, "Turn permission
  prompts off in every mode"). `TestEveryShippedAgentRunsWithoutPermissionPrompts`
  enforces it.
- **What remains:** questions and elicitations (25 fix headers), and pending
  ACP requests dropped on restart (`aether:docs/enhanced-runs.md:215-224`).
- **Design choice.** Aether removes the tool-approval class by policy (the
  container is the boundary). Omnigent keeps approvals as a product feature
  (39 `fix(policies)` PRs) and pays for it.

### 2.4 Resume and reattach

**Omnigent:**

- **Mechanism.** Cold resume runs `claude --resume <sid>`, which is bound to
  the original cwd. Codex `thread/resume` is version-gated.
- **Fixes:**
  - `b4d63e860` (#10961): a resume finds no rollout.
  - `4e7aed70c` (#9518): resume lineage not paginated.
  - `c418d41db` (#6270): fall back to a fresh thread.
  - `2b75b0f3d` (#6364): compacted context lost.
  - `d05a4a775` (#6025): history 500s.
  - `aa533c30c` (#10329): throttled history pages.
- **Open issues:**
  - #10377: managed sessions not resumed after a host restart.
  - #10554: Claude cold resume after compaction returns 400.

**Aether:**

- **Mechanism:**
  - The server-side PTY outlives its clients.
  - The server reattaches to live containers after it restarts.
  - Enhanced sessions restore by resume, then load, then a new session
    (`aether:internal/acphost/session.go:336-370`).
  - Integration chaos tests SIGKILL the server
    (`aether:internal/server/chaos_reboot_integration_test.go`).
- **Fixes:**
  - `147150ce` (#353): reboot mislabel.
  - `b15bccfa` (#382): background tasks shown running after a crash.
  - `e47bba88` (#309): unknown container users after restart.
  - `6f4b316a` (#157): shutdown during recovery.
  - `09cd12d7` (#130): recovered PTY attachable before its scheduler entry.

Comparison:

- At 7.8% of fix headers this is Aether's second-largest class; Omnigent's is
  5.8%.
- Aether's topology is simpler: one server owns the containers and PTYs.
- Omnigent's laptop hosts, runners and tunnels add a class Aether lacks:
  tunnel and network, 297 fix headers. Its resilience lab exists because of
  them.

### 2.5 Auth expiry

**Omnigent:**

- Auth is 8% of sampled issues and 6.1% of fix headers.
- Omnigent mints its own credentials on top of the vendor logins: host
  tunnel tokens, hook tokens, launch bearers and Databricks gateway tokens.
- Issues:
  - #3012: permanent 403 after session-JWT expiry.
  - #10699: pi-native 403 after the launch bearer expires.
  - #3486: policy-hook credentials never refreshed.
  - #11417 (open): OIDC with Entra ID.

**Aether:**

- Auth is 2.7% of fix headers.
- The vendor login stays in the member home, and Aether does not proxy model
  credentials.
- Its fixes concern borrowed logins (`b01816e1`, #280: Claude's onboarding
  wizard ignores a mounted login).
- (inference) Fewer minted credentials means a smaller class.

### 2.6 Transcript and event loss

**Omnigent:**

- **Mechanism.** It tails private vendor files: Claude JSONL, Cursor
  `store.db`, Hermes `state.db`.
- **Fixes:**
  - `1f1cf2177` (#6844): ambiguous POSTs were skipped instead of retried,
    losing user messages; its e2e test is
    `omnigent:tests/e2e/test_claude_native_forwarder_user_message_loss_e2e.py`.
  - `35ba9e32a` (#10293): Claude 2.1.300 started writing api-request records.
  - `021d19951` (#9048): child transcripts lost in outages.
  - #1120: 681 silent forwarder failures.

**Aether:**

- **Mechanism.** Aether records what passes through its own PTY (an
  asciinema cast) and its ACP pipe (an item log), so vendor format changes
  cannot cause losses here.
- **Its own bugs remain:**
  - Replay: `25399085` (#165), `9c40d2e6` (#186), and `40d1c01b` (#365, an
    OOM allocation).
  - Late ACP tool output (`5bc9258b`).

### 2.7 Sub-agents and handoff

**Omnigent:**

- 6.2% of fix headers.
- Cross-harness child sessions are a headline feature.
- Issues:
  - #10361 (open): the idle reaper kills a working Codex sub-agent.
  - #5334: "idle launders failed into completed".
  - #3268: headless Codex sub-agents never start.

**Aether:**

- 2.3% of fix headers.
- Subagents are shown, not orchestrated (#415).
- Codex nesting is blocked upstream: codex-acp drops
  `clientCapabilities.subagents`.

### 2.8 Inter-agent message delivery: Aether's largest class

**Aether:**

- **Scale.** 103 fix headers (7.8%).
- **Fixes:**
  - `5a7d5091` (#204): workers marked Done while humans were away.
  - `2a62a668` (#214).
  - `f2c7f904` (#224): integrator notices typed into a shell were run as
    globs.
  - `84b8e536` (#238): mission runs could not message conflict-radar peers.
  - `cca0276c` (#258): wake boundaries.
  - `610e5641` (#332): a completed swarm refused its integrator.
  - `8cf03851` (#518): owner messages waited 45 s.
- **Open audit issues:**
  - #225: nothing detects a dead integrator.
  - #226: no end state after active.
  - #227: replace-integrator leaves the old one running.
  - #230: a killed worker gets a `wip:` commit.
- **Live evidence from this run.** `aether-internal skill` printed `Inbox
  hooks (claude): missing (missing registrations: SubagentStart)`. Team note
  `note-yb27g8gky6` records that `send` is rate limited to a burst of 5, then
  one message per 5 s.
- **Root cause.** In Standard mode, mail reaches an agent only at a vendor
  hook boundary or through idle wake. The two vary per agent:
  - Codex and Copilot have no idle wake.
  - Gemini and Cursor hooks are installed by hand.
  - Antigravity has nothing (`aether:docs/harnesses.md:18-28`, `:486-518`).

  One feature thus behaves nine different ways.
- **Enhanced does not have this problem.** The server prompts the idle
  session itself (`aether:docs/enhanced-runs.md:201-213`).

**Omnigent:**

- 1.1% of fix headers.
- It has no peer swarm. Messages flow parent to child through a runner inbox:
  `6c6fa6884` (#1770) and `f1bab8ce5` (#9612, wake Codex parents).

**For Aether:**

- Make Enhanced the default for every swarm worker and integrator whose agent
  has an adapter.
- Treat Standard-mode mail as best-effort, and show that state in `aether-internal status`.

### 2.9 Harness version drift: is tmux observation more exposed than ACP?

Yes, it is more exposed. Omnigent's Claude bridge matches:

- glyphs: `❯`, `!`, box rules;
- footers: "Enter to … · Esc to …", "Esc to cancel";
- dialog titles: "Do you want to ", "Yes, and don't ask again", "Teach auto
  mode about your environment?";
- a full billing-notice paragraph
  (`omnigent:omnigent/harnesses/claude_native/bridge.py:268-500`).

Its own comments record the drift: "the search chrome's casing has already
drifted across releases" and "Claude Code has framed the input box both ways
across versions" (`:286-289`, `:481-483`).

Fixes naming the harness release that broke Omnigent (body quotes, hand-checked):

| Commit | Harness change |
| --- | --- |
| `2be4c6f63` (#11379) | Claude Code 2.1.308 asks "Make auto mode your default permission mode?" at startup |
| `434acbe84` (#10692) | On 2.1.299, Escape on the "Settings Error" dialog exits Claude |
| `35ba9e32a` (#10293) | 2.1.300 writes api-request records into transcripts |
| `332daef59` (#6202) | Since 2.1.212 the ctrl+r search rides the composer |
| `b8687ebe4` (#5283) | Against 2.1.240 the composer detector misread three surfaces |
| `cf34d7b90` (#4132) | ≥2.1.197 writes `status: "shell"` |
| `d8faa1c4c` (#5757) | A Claude release backticks model names and appends `(default)` |
| `b77990a1c` (#8828) | Codex 0.159.0 renamed `--remote` |
| `4d963a360` (#7506) | Codex 0.154.0 tightened the `thread/fork` contract |
| `a77807e56` (#5648) | Codex 0.150.1 emits a second `thread/started` |
| `221a09d3e` (#8698) | Newer Codex paginated-history lineage |
| `c418d41db` (#6270) | Newer Codex rephrased a resume error |
| `9d119233d` (#1250) | Codex 0.140.0-alpha.2 turn.error shape |
| `a3cd0e39c` (#8388) | Pi ≥0.80.4 emits several `agent_end` per prompt |
| `5c4738964` (#10257) | Pi 0.84.2 has no `/thinking` |
| `d9eb6458f` (#2529) | Pi `--approve` arrived in 0.79.0 |
| `ce6dba9c8` (#4725) | OpenCode 1.18.x rejected by the version gate |

Issues: #10087 (Codex 0.156+), #5632 (Codex 0.150.1, unusable), #3011
(kiro-cli 2.13.0), #11154 (agy 1.4.4, open), #9439 (Claude 2.1.293
`/auto-mode-setup`, message lost), #10691 (Claude 2.1.299).

**Omnigent's mitigations:**

- Version floors (Claude 2.1.161,
  `omnigent:omnigent/onboarding/harness_install.py:103-105`).
- CLIs pinned in CI (Claude 2.1.283, Codex 0.142.0, Pi 1.0.4;
  `omnigent:.github/actions/setup-harness-cli/manifest.json`).
- Manual-only Dependabot bumps for harness CLIs
  (`omnigent:.github/dependabot-automerge.json`).
- Codex version gates (`omnigent:omnigent/harnesses/codex_native/app_server.py:178-185`).
- (inference) Users run any version above the floor, so a vendor release
  reaches them before the CI pin moves.

**Aether's exposure:**

- **Drift fixes found:** `e3efa36c` (#83; current Claude Code refused
  `--print --output-format stream-json` without `--verbose`, so every
  headless claude run died), `b01816e1` (#280, onboarding wizard), and
  `b946c95e` (Gemini folder-trust prompt).
- **Enhanced:**
  - The adapters are pinned (claude-agent-acp 0.86.0, codex-acp 2.1.1, pi-acp
    0.0.34; `aether:internal/harness/acp.go:36-41`), and the ACP protocol
    version must match exactly (`aether:internal/acphost/conn.go:396-397`).
  - The built-in ACP servers (opencode, omp, copilot, gemini, cursor) are not
    pinned.
  - `TestACPAdapterInstall` runs in no workflow: nothing in `.github/` sets
    `AETHER_ACP_IMAGE`.
- **Standard:**
  - Agents auto-update before every launch
    (`aether:docs/harnesses.md:275-300`).
  - The smoke image is deliberately unpinned
    (`aether:images/smoke/Dockerfile:6-9`). It catches argv drift in CI only
    after users already have the new version, and checks no hook schema.

**Answer:** tmux observation depends on more undocumented contracts than ACP:
screen text, private files and private databases. ACP depends on one
versioned protocol and a pinned adapter. Aether's weakness is not the design;
it ships unpinned vendor updates to users with no gate.

**For Aether:**

- Record a verified version per agent.
- Update past it only after the smoke and hook tests pass on the new version
  in CI.
- Pin the built-in ACP servers the same way as the adapters.

### 2.10 Sandbox, container and process lifecycle

**Omnigent:**

- Runs harnesses on user hosts in tmux, with no container by default.
- 92 fix headers about orphans, leaks or reaping:
  - `6102fa585` (#11289): reap idle panes.
  - `f68abff46` (#3925): leaked codex app-servers.
  - `8e4292e5e` (#5866): orphaned bridge dirs.
  - `fd551d785` (#1923): per-uid tmp.
- Issues:
  - #8077: native sessions leak until the host halts; closed after 30 days.
  - #561: orphaned app-servers and tmux servers.
  - #11357 (open): orphaned `opencode serve`.

**Aether:**

- One container per run. No fix header concerns a leaked or orphaned
  process; the one match for "orphan" is a dashboard control.
- Its lifecycle fixes are about disk, cache and evidence limits: `7efa92fc`
  (#328), `e44341e6` (#501), and `14cb6232` (#136).

**Verdict:** the container boundary eliminates this class by design. Keep it.

## 3. Which integration layer is empirically more reliable

- **Omnigent, as shipped to its users.** It is not more reliable in
  failure-class terms. It has the same class mix, a rising harness-fix rate,
  443 open bug issues, and its most-used harness path is its main bug source.
  It is far better at turning failures into evidence: sampled bug issues
  close in a median 4.6 days, and its triage bot marked 206 of 600 sampled
  issues `reproduced`.
- **Aether's ACP path.** Structurally it is the more reliable design.
  Empirically it is under-tested:
  - acpmock replays recorded conversations and never runs a real adapter in
    CI.
  - Its recorded failures are races in Aether's own turn accounting (#490,
    #363), not lost protocol messages.
- **Aether's Standard path and coordination layer** are where the "flaky"
  experience comes from (sections 2.1, 2.2 and 2.8). The low fix counts there
  measure missing verification and instrumentation, not robustness
  (inference, supported by `aether:internal/scheduler/driver.go:51-52`).

## 4. Reliability tooling: what Omnigent has and what to copy

| Omnigent tool | What it does | Aether today | Copy? |
| --- | --- | --- | --- |
| Resilience lab | Real server, host and runner, plus the real Claude Code and Codex TUIs, a mock model, and a fault proxy on every link (`omnigent:tests/e2e/resilience/README.md:1-14`, `omnigent:tests/e2e/resilience/lab/proxy.py:301`). Scenarios S1-S7 define a user-visible contract; gaps are pinned as strict xfail (`omnigent:tests/e2e/resilience/scenarios/_contract.py:55-75`). Short scenarios run in the PR e2e gate. | Chaos tests SIGKILL the server and apply disk pressure and stalls, with scripted `fake` agents (`aether:docs/testing.md:120-145`) | **Yes, first.** Run pinned real agents plus adapters against a mock model, kill and partition the ACP pipe, SSH and edge links, and pin the known gaps (pending approval on restart, Not sent queue) as strict expected failures |
| Real CLIs against a mock model in the PR e2e gate (`omnigent:.github/workflows/e2e.yml:1-25`, `omnigent:.github/actions/e2e-run/action.yml:102-128`) | Catches TUI, hook and protocol breakage without credentials | Smoke checks argv only; logged-in smoke, `ACP_LIVE` and `TestACPAdapterInstall` are manual (`aether:docs/testing.md:148-213`) | **Yes.** Replace acpmock-only Enhanced coverage with real pinned adapters driven by a mock Anthropic/OpenAI endpoint |
| Input-delivery tracing (`omnigent:docs/NATIVE_INPUT_DELIVERY_DIAGNOSTICS.md:14-97`) | Stable IDs per input from enqueue to settlement, with `cause_class` | `delivered` callback only; no settled outcome for PTY writes | **Yes**, for steer, mail prompt and ACP prompt; key the settlement on prompt receipt, not on text |
| Harness bench (`omnigent:docs/harness-bench-design.md:16-36`, `omnigent:tests/harness_bench/verdict.py:112`) | Live probes reconciled against declared capability flags; `DRIFT` fails | Hand-maintained capability table (`aether:docs/harnesses.md:18-28`) | **Yes.** Generate that table from probes, and fail on drift. Note: Omnigent never wired the live bench into CI |
| Pre-cleanup failure snapshots (`omnigent:docs/harness-diagnostics.md:142-221`) | Exit status, stderr and last screen captured before teardown | 4 KiB ACP stderr tail (`aether:internal/scheduler/acp_driver.go:35`); a Standard agent exit just opens a shell | **Yes**, cheap |
| Event-loop watchdog (`omnigent:omnigent/util/event_loop_watchdog.py:111`) | Logs stacks on a 5 s stall | Manual "capture an unresponsive host" script (`aether:docs/failure-handling.md:158-216`) | Adapt: a lock-hold and goroutine-dump watchdog for scheduler and store locks |
| Outage telemetry keyed by `outage_id` (`omnigent:docs/runner-recovery-measurement.md:31-48`) | Measures recovery with lower and upper bounds | Edge metrics only | Later; the topology is smaller |
| Flake stress (`omnigent:.github/workflows/flake-stress.yml`), nightly failure monitor, issue triage and repro bot | Flake rates as data; bugs reproduced automatically | Manual reruns (memory notes on CI flakes) | Flake stress: yes. Triage bot: only once there is an issue flow |

Do not copy:

- tmux probe capture (`omnigent:docs/terminal-probe-error-capture.md`):
  Aether has no tmux.
- The screen-reading delivery confirmation: route around the TUI instead.

## 5. Eliminate by design in Aether, ranked

1. **Blind Standard steer.** Send `aether message`, connector and assistant
   messages to Standard runs through the mailbox plus native wake. Keep raw
   typing as an explicit "type into terminal" action that brackets pastes
   (section 2.1).
2. **Hook-dependent mail.** Default swarm runs to Enhanced wherever an
   adapter exists. Surface "mail waits for next hook" as a visible state
   (section 2.8).
3. **Unpinned vendor drift.** Gate auto-update on a CI-verified version, pin
   the built-in ACP servers, and run `TestACPAdapterInstall` with `ACP_LIVE`
   against a mock model in CI (section 2.9).
4. **Fire-and-forget status.** Spool reporter events with an idempotency key.
   Count a run that ends a turn without `aether-internal report` as its own
   reason, not as generic idle (section 2.2).
5. **Bundled fixes.** One failure per PR, each with a regression test named
   after the failure. Omnigent's `test_claude_native_*_e2e.py` files show the
   pattern. This makes the next comparison measurable.

## 6. Method

Commands, run read-only with bounded time:

```sh
git clone --filter=blob:none https://github.com/omnigent-ai/omnigent /tmp/omnigent
git -C <repo> log --format='%h%x09%ad%x09%s' --date=short          # subjects
git -C <repo> log --format='@@@%h%x09%ad%n%B' | grep '^\* fix'      # sub-fix headers
git log --format=%h -- internal/harness internal/acphost ...        # Aether integration set
gh api 'search/issues?q=repo:omnigent-ai/omnigent+is:issue+label:Bug' --jq .total_count
gh api -X GET search/issues -f q='repo:omnigent-ai/omnigent label:Bug created:<window>' -f per_page=100
gh issue list --repo 3xDevOps/aether --state all --limit 100
```

Classifier rules, first match wins, applied to the subject without its type
and scope:

1. approval: `approv|permission|elicitation|askuserquestion|plan dialog|don't ask|pending prompt|needs input|bypass`
2. inter-agent: `inbox|mailbox|peer|integrator|swarm|mission|wake|coord`
3. sub-agent: `sub-agent|child|handoff|side chat|nested|fork|parent|background task|worker`
4. input: `deliver|paste|inject|submit|composer|input box|keystroke|enter|swallow|draft|slash command|steer`
5. resume: `resume|reattach|reconnect|relaunch|restart|reboot|recover|restor|respawn`
6. auth: `auth|login|sign-in|token|oauth|credential|expired|password`
7. transcript: `transcript|jsonl|rollout|history|replay|backfill|forward|mirror|dedup|duplicat|lost`
8. status: `status|stuck|idle|busy|working|spinner|hang|freez|stale|ready|title|outcome|interrupted`
9. drift and TUI: `dialog|footer|popup|panel|escape|picker|startup prompt|old-cli|version|upgrade|compat|wizard|onboarding|tui`
10. lifecycle: `sandbox|container|docker|reap|orphan|zombie|leak|oom|kill|teardown|cleanup|daemon|process|exit|retain|pool|cache|disk`
11. network: `tunnel|websocket|network|disconnect|timeout|heartbeat|proxy|throttl|429|5xx|outage|transient|retry|gateway|edge|connection`
12. terminal: `terminal|tmux|pty|xterm|scroll|resize|viewport|pane`

Spot checks of 12 random items per class found typical misfiles. A
pi-native "trust dialog" flag is counted as approval, though it is really a
startup dialog. A "cached tokens" billing fix is counted as auth.

The drift list in section 2.9 and the issue examples were checked by hand. The
Omnigent issue classification used 600 bug issues, the latest 100 of each of
six windows: through Oct 25, Jun 13-25, Jul 19-31, Aug 23-31, Sep 23-30 and
Oct 15-18. 330 keyword labels were overridden by hand and 10 bodies were read.
