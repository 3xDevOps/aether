# Choose the model for a run

**Status:** Planned.

**Goal:** Whoever launches a run picks the agent's model: from the New run
dialog, a swarm, a template or the CLI. The choice survives a reopen, a
resume, a mode switch and a recovery relaunch.

A **run** is one agent working in one container on a workspace's code. Its
**harness** is the agent CLI it runs (Claude Code, Codex, pi, oh-my-pi,
OpenCode), described by a harness profile in `internal/harness/harness.go`.
A **Standard** run drives that CLI in a terminal; a **Background** run starts
it with a task and no terminal; an **Enhanced** run talks to it over ACP (the
Agent Client Protocol) and renders the session in the dashboard. A **swarm**
is one integrator run that splits an objective into tasks and starts worker
runs for them. A **template** is a saved launch.

## 1. Today

Nobody picks a model at launch. The only control is the composer menu of an
Enhanced run after it has started (`run.acp.set_option` →
`session/set_model`), and reopening the session does not re-apply it. A
swarm cannot say "Opus for the integrator, a cheap model for workers".
Templates and the CLI have no model.

## 2. Decisions

1. A **model** is a free-text string per launch. Empty means the agent's own
   default, which is today's behaviour everywhere. There is no curated
   catalog; the dashboard suggests values per agent (Claude Code: `opus`,
   `sonnet`, `haiku` or a full id; the others as their CLI documents).
2. `domain.ValidModel` accepts empty, or at most 128 bytes with no
   whitespace or control characters and no leading `-`. Anything else is the
   agent's business: it reports a bad model and Aether shows that error.
3. The model is set in the New run dialog, the Swarm tab, templates,
   `aether run --model`, `aether swarm create --model`,
   `--worker agent[:mode[:model]]`, `aether template save --model` and
   `aether-internal worker start --model`.
4. Model is part of a swarm's execution choice. The tuple becomes
   (account, harness, mode, model), so a swarm may mix `claude`+`opus` and
   `codex`+`gpt-5-codex`.
5. Standard, Background and resume launches pass the model as the agent's
   CLI flag. The flag goes before the task prompt in argv, so a CLI that stops
   parsing options at the prompt still reads it; a resume command has no
   task, so the flag is appended.
6. An Enhanced run sets the model after the session opens, where the
   starting mode is set today (`sess.SetMode` in
   `internal/scheduler/acp_driver.go`). It uses the agent's native `model`
   config option when advertised, else `session/set_model`. A failure is an
   error notice in the session, never ignored.
7. A harness with no model flag (a custom `--agent-definitions` definition
   with its own executable, or `fake`) refuses a non-empty model at launch
   with an error naming the agent. An argv override of a shipped agent name
   keeps that agent's model flag, because it runs the same CLI.
8. The model is re-applied on reopen of an Enhanced session, on resume, on a
   Standard↔Enhanced switch (the resume argv) and on a recovery relaunch of
   a swarm run. A model picked from the composer menu updates the run's
   recorded model (`SetRunModel`), so a reopen keeps it.
9. The run header shows the model next to the agent and mode.

## 3. Per-agent flags

Each row was read from the installed CLI's help on 2026-10-11.

| Agent | Standard, Background, resume | Enhanced | Source |
| --- | --- | --- | --- |
| `claude` | `--model <model>` (alias such as `opus`, `sonnet`, or a full name) | `model` option, else `session/set_model` | `claude --help`, 2.1.296 |
| `codex` | `-m, --model <MODEL>` on `codex`, `codex exec`, `codex resume` | `model` option, else `session/set_model` | `codex --help`, `codex exec --help`, `codex resume --help`, codex-cli 0.162.1 |
| `pi` | `--model <pattern>` (`provider/id`, optional `:<thinking>`) | `model` option, else `session/set_model` | `pi --help`, 1.1.0 |
| `omp` | `--model=<value>` (fuzzy: `opus`, `gpt-5.2`, `openai/gpt-5.2`) | `omp acp` has no model flag; `model` option, else `session/set_model` | `omp --help`, `omp acp --help`, omp/18.8.9 |
| `opencode` | `-m, --model provider/model` on `opencode` and `opencode run`, plus `model` in `OPENCODE_CONFIG_CONTENT` | `opencode acp` has no model flag; `model` in `OPENCODE_CONFIG_CONTENT`, then the session option | `opencode --help`, `opencode run --help`, `opencode acp --help`, 1.19.0 |
| custom definition, `fake` | refused | refused | no model flag |

OpenCode reads `OPENCODE_CONFIG_CONTENT` on every launch, and `opencode acp`
reads nothing else for a model, so every opencode launch merges the `model`
key into it with `harness.MergeEnv`. The launch-level value is merged after
the workspace's variables, so an explicit per-run model wins over a
workspace default.

## 4. Where the model lives

All protocol fields are `model`, `omitempty`.

- Protocol: `RunLaunchParams.Model`, `Run.Model`,
  `MissionExecutionChoice.Model`, `MissionIntegrator.Model`,
  `WorkerStartParams.Model`, `Attempt.Model`, `Template.Model`,
  `TemplateSaveParams.Model`.
- Domain: `Run.Model`, `LaunchOptions.Model`, `MissionExecutionChoice.Model`,
  `MissionIntegrator.Model`, `AttemptReservation.Model`, `Attempt.Model`, the
  template's `Model`. `scheduler.MissionLaunchSpec`,
  `mission.MissionLaunchRequest` and `ValidateMissionLaunch` gain a model.
- Harness: `Profile.ModelArgs`, a token template with a `{model}`
  placeholder, and OpenCode's model config env.
- Store, migration v59: `runs.model`, `mission_attempts.model`,
  `missions.integrator_model`, `mission_create_receipts.integrator_model`,
  `mission_integrator_replacements.model`, `templates.model`. The
  `execution_choices` JSON blob gains `model`, omitted when empty, so stored
  blobs and receipts written before v59 compare equal.

## 5. Tuple comparisons that change

Every place below compares or copies (account, harness, mode) and gains
model. Line numbers are as of `008ff4c4`.

- `internal/mission/service.go`: :940 `executionChoice` (the integrator must
  equal one of the choices); :344-345 and :357-358 recovery staleness checks
  for the integrator and the attempt; :759-762 choice parsing on create;
  :926 `replacementChoices`; launch requests at :445, :564, :797, :895.
- `internal/mission/workers.go`: :53 choice match on `worker start`; :81
  reservation; :113 launch.
- `internal/mission/assignments.go`: :159 the integrator's assignment
  choices.
- `internal/store/mission.go`: :132 `missionChoiceValid`; :147 dedup key;
  :222 create receipt comparison; :436 replace-integrator receipt
  comparison.
- `internal/store/mission_attempts.go`: :47 `attemptSemanticallyEqual`.
- `internal/scheduler/mission.go`: :153 `sameReservedRun`.
- `internal/coordcli/mission.go`: :258 `worker start` choice selection by
  `--harness`, `--mode`, `--account-owner-id`, now also `--model`.
- `cmd/aether/swarm.go`: :135-145 `parseWorker`; :160-170
  `missionCreateParams` dedup (struct equality).
- `web/src/components/launch/launch-dialog.tsx`: :161-172 integrator and
  worker dedup and the sorted `execution_choices` (the sort adds model);
  `sameWorker` in `web/src/components/launch/worker-agents.tsx:12`;
  `web/src/routes/missions/dialogs.tsx`: :42-62 replace-integrator choice
  dedup.

## 6. Changes by area

### Harness registry

`internal/harness/harness.go` gives each shipped profile `ModelArgs`.
`Argv` inserts the expanded model args before the first token carrying the
task placeholder; with no task it appends them.

### Scheduler

`command()` refuses a model for a profile without `ModelArgs` and assembles
argv and env. The resume argv in `mode_switch.go` carries the run's model.
The ACP driver sets the model after open and posts a failure notice.
`ACPSetOption` records the model the composer picked.

### Store and protocol

The fields and columns in section 4, in one migration.

### Mission

The execution-choice tuple has four fields everywhere in section 5. The
integrator, each attempt and each replacement record their model, and
recovery relaunches with it.

### CLI

`aether run --model`, `aether template save --model`,
`aether swarm create --model` for the integrator,
`--worker agent[:mode[:model]]`, and `aether-internal worker start --model`.

### Dashboard

A **Model** field beside Mode in New run, remembered per agent like Mode:
the persisted `launchDefaults` entry becomes `{mode, model, at}`. A model
per integrator and per worker row in the Swarm tab. A Model field in the
template editor. The model in the run header.

### Docs

`docs/harnesses.md` (the flag table), `docs/enhanced-runs.md`,
`docs/dashboard-frontend.md`, `docs/teams.md`, `docs/coordination.md`,
`docs/local-gateway.md`, `docs/testing.md`.

## 7. Testing

- Harness: argv tests per profile for a task launch, a taskless launch and
  resume, with and without a model.
- Scheduler: launch tests against the fake runtime asserting the container
  argv and the OpenCode config env; refusal for a profile without
  `ModelArgs`; re-application on reopen and mode switch.
- Mission: tuple tests for choice match, dedup, receipt comparison and
  recovery staleness, with two choices that differ only in model.
- Store: v59 round trip, and a pre-v59 receipt that still matches.
- Web: one test per new field (New run, Swarm row, template editor).

## 8. Out of scope

- A curated model catalog.
- Validating a model against what the agent advertises.
- Model flags for custom definitions.
- Effort and reasoning settings; see
  [effort-per-run](2026-10-27-effort-per-run.md).
