# Choose the effort for a run

**Status:** Planned.

**Goal:** Whoever launches a run picks the agent's effort or thinking level
next to its model: from the New run dialog, a swarm, a template or the CLI.
The first turn runs at it, and the choice survives a reopen, a resume, a mode
switch and a recovery relaunch, exactly like the model
([model-per-run](2026-10-11-model-per-run.md)).

## 1. Today

Nobody picks an effort at launch. An Enhanced run can change it from the
composer's effort menu, but only after the first turn has already run at the
agent's default, and a reopen does not re-apply it.

## 2. Decisions

1. An **effort** is a string per launch. Empty means the agent's own default,
   which is today's behaviour everywhere.
2. Unlike a model, an agent's effort levels are a closed set, so each shipped
   harness profile lists them (`Profile.EffortLevels`) and `agent.list`
   returns them as `effort_levels`. The dashboard offers the picker only for
   an agent with levels, and only those levels.
3. `domain.ValidEffort` accepts empty, or at most 32 bytes with no whitespace
   or control characters and no leading `-`. A launch then refuses a level
   the agent does not list, and an agent with no effort flag refuses any
   effort outside an Enhanced run, both with an error naming the agent.
4. Effort is part of a swarm's execution choice. The tuple becomes
   (account, harness, mode, model, effort).
5. Standard, Background and resume launches pass the effort as the agent's
   CLI flag, after the model flag and before the task prompt.
6. An Enhanced run sets the effort after the session opens, after the model,
   since an agent's levels can depend on the model. It sets the agent's
   session config option of category `thought_level`. Copilot's ACP server is
   its own CLI and reads `--reasoning-effort`, so a Copilot Enhanced run
   passes the flag instead. A failure is an **Effort not set** notice; a
   Background run driven over ACP fails rather than run unattended at the
   default.
7. An effort picked from the composer menu becomes the run's recorded
   effort, so a reopen keeps it, and is published as `run.effort`. Claude's
   adapter offers `default`, which `claude --effort` refuses, so it is
   recorded as empty.
8. The run header shows the effort after the model.
9. Saved launch defaults do not remember an effort: every launch starts at
   the agent's default unless the launcher picks one.

## 3. Per-agent settings

Each row was read from the installed CLI's help and from the session options
its ACP server advertises, on 2026-10-27.

| Agent | Standard, Background, resume | Levels | Enhanced | Source |
| --- | --- | --- | --- | --- |
| `claude` | `--effort <level>` | `low`, `medium`, `high`, `xhigh`, `max` | `effort` option | `claude --help` 2.1.311, claude-agent-acp 0.86.0 |
| `codex` | `-c model_reasoning_effort=<level>` | `low`, `medium`, `high`, `xhigh`, `max`, `ultra` | `reasoning_effort` option | codex-cli 0.164.0 `codex debug models`, codex-acp 2.1.1 |
| `pi` | `--thinking <level>` | `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` | `thought_level` option | `pi --help` 1.4.0, pi-acp 0.0.34 |
| `omp` | `--thinking=<level>` | pi's, plus `auto` | `thinking` option | `omp --help` 18.13.1, `omp acp` |
| `copilot` | `--reasoning-effort <level>` | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` | the same flag on `copilot --acp` | `copilot --help` 1.0.104 |
| `opencode`, `gemini`, `cursor`, `antigravity`, custom, `fake` | none | none | none advertised | `opencode acp` 1.19.4, `gemini --help` 0.65.0 |

Codex accepts any string and leaves the check to the model: not every model
takes every level (`codex debug models` lists each one's
`supported_reasoning_levels`), and a level the model refuses is the agent's
error. `opencode run --variant` exists, but neither the OpenCode TUI nor
`opencode acp` takes one, so OpenCode is not offered an effort.

## 4. Where the effort lives

Every place section 4 of the model plan lists gains `effort` next to
`model`, `omitempty` on the wire. Migration v93 adds `runs.effort`,
`mission_attempts.effort`, `templates.effort`, `missions.integrator_effort`,
`mission_create_receipts.integrator_effort` and
`mission_integrator_replacements.effort`. The `execution_choices` JSON gains
`effort`, omitted when empty, so stored choices and receipts written before
v93 compare equal. `AgentInfo.EffortLevels` is new.

The harness profile gains `EffortArgs` (a token template with `{effort}`),
`EffortLevels` and `ACPEffortArgs`. A shipped-name override keeps all three,
as it keeps the model flag.

## 5. Changes by area

- **Harness:** the profiles and `CheckEffort`; `LaunchArgs(model, effort)`
  replaces the model-only argv helper.
- **Scheduler:** `command()` checks and inserts the effort; the resume argv
  carries it; the ACP driver sets it after the model; `ACPSetOption` records
  a composer pick.
- **Store and protocol:** the fields in section 4.
- **Mission:** every tuple comparison in section 5 of the model plan gains
  effort.
- **CLI:** `--effort` on `aether run`, `aether template save`,
  `aether swarm create`, `aether swarm replace-integrator` and
  `aether-internal worker start`; `--worker agent[:mode[+effort][:model]]`
  puts a worker's effort on the mode, because a model may itself contain
  `:` (pi's `sonnet:high`) or `@` (Vertex ids).
- **Dashboard:** an **Effort** field beside Model in New run, the Swarm tab
  (integrator and each worker row) and the template editor; the effort in
  the replace-integrator choices and the run header.
- **Docs:** `docs/harnesses.md`, `docs/enhanced-runs.md`,
  `docs/dashboard-frontend.md`, `docs/teams.md`, `docs/coordination.md`,
  `docs/local-gateway.md`.

## 6. Testing

- Harness: argv per profile for a task launch, a taskless launch and resume,
  with and without an effort; refusal of an unlisted level and of an effort
  for an agent with no flag.
- Scheduler: container argv against the fake runtime; the Enhanced option set
  after open, its failure notice and the Background failure; re-application
  on reopen and mode switch; a composer pick recorded.
- Mission: tuple tests with two choices that differ only in effort.
- Store: v93 round trip, and a pre-v93 receipt that still matches.
- Web: one test per new field.

## 7. Out of scope

- Remembering an effort in saved launch defaults.
- Checking an effort against what the chosen model supports.
- Effort settings for custom definitions.
- An effort for child runs and for launches through `internal/ops` (the
  assistant and connectors): a child started from a swarm choice runs at the
  agent's default effort.
