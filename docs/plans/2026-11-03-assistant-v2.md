# Assistant v2: Proposal

Status: proposal, no code. Builds on
[2026-11-03-assistant-signin-and-standing-jobs.md](2026-11-03-assistant-signin-and-standing-jobs.md)
(assistant sign-in and `relay_to` on watches, being built now) and on
[assistant.md](../assistant.md), [watches.md](../watches.md),
[connectors.md](../connectors.md), [security.md](../security.md) and
[teams.md](../teams.md).

## Recommendation

Keep one assistant per member, and make the server, not the model, own
everything that has to be durable or trusted:

1. **One conversation, many doors.** The dashboard, the phone and the
   desktop app already share it. Add a server-side prompt queue that labels
   each message with its origin, and an `aether_ask_assistant` connector tool
   that feeds the same queue. Approval cards outlive the turn that raised
   them, so you can approve on your phone after a ChatGPT turn has ended.
2. **Approvals.** The first use asks "ask before acting" or "act without
   asking". Every card gains **Always approve this kind** (a typed rule,
   matched by the server, listed and revocable in Settings) and **Approve
   everything from now on** (today's auto-approval with today's confirm).
   Today's always-ask cases stay.
3. **Teams.** Assistants never talk to each other directly. They file typed
   requests (`notify`, `ask`, `handoff`) with the server. Free text goes only
   in a capped `body` that is always quoted as untrusted. The recipient's
   answer is drafted in a separate context that holds none of their private
   data, and it leaves only when the recipient clicks. A turn that read a
   teammate's request can never auto-approve anything.
4. **Event-driven.** Watches and `relay_to` do all the waiting. A follow-up
   that needs no judgement ("when A finishes, launch B") is approved once and
   then run by the server. The model wakes only for judgement, within a daily
   budget. "Needs you" pushes go out batched, at one of three levels you
   choose, and the server writes the digest without a model.

| Phase | What | Size | Depends on |
| --- | --- | --- | --- |
| 0 | Sign-in and `relay_to` (other session) | M | in progress |
| 1 | Setup choice, card options, scoped rules, Settings and CLI | M, 2 PRs | none |
| 2 | Durable cards and a prompt queue with origins | M, 2 PRs | none |
| 3 | `aether_ask_assistant` connector tool | S, 1 PR | 2 |
| 4 | Notification levels, batching, approve from a push | M, 2 PRs | 2 |
| 5 | Server-run follow-ups, judgement wakes, turn taint | L, 3 PRs | 0, 2 |
| 6 | Team requests: notify, ask, handoff, request desk | L, 4 PRs | 1, 2, 4, 5 (taint) |
| 7 | Injection and leak evals in `internal/assistant/eval` | S–M, with 5 and 6 | none |

Phases 1–4 fix the daily friction for one person running many agents and
can ship in any order. Phase 5 removes external polling. Phase 6 is the team
feature and needs the taint rule from phase 5 first.

## What is true today

- **One conversation per member**, in `aether-assistant-<member>`, on the
  assistant's own sign-in. The Android and desktop shells wrap the same
  dashboard, so the phone already opens the same conversation. The real gap
  is ChatGPT and Claude: the connector exposes direct tools, holds its own
  context, and cannot reach the assistant's conversation or preferences.
- **One turn at a time.** A prompt sent during a turn fails with `assistant:
  a turn is running; stop it first` (`internal/assistant/assistant.go`).
- **Cards are tied to the turn.** A card expires after 10 minutes, and
  ending the turn withdraws it (`confirm.go`, `confirmTTL`). Nobody can
  approve later or from another door.
- **Auto-approval** covers `run.launch`, `run.inject`, `run.kill` and
  `env.save` (`autoApproved`). It never covers other members' sessions, a
  launch with automatic fallback while any account is shared with you, or a
  card whose text holds a saved preference (`carriesPreference`).
- **The nudge** appears after 3 cards in a row that auto-approval would have
  passed, then snoozes for 7 days, then 30, then stops (`suggestAfter`).
- **Preferences** are private to the member: teammates, admins, sessions
  and connectors cannot read them ([privacy.md](../privacy.md#assistant-preferences)).
- **Sessions are not private.** Every member can read every session on the
  server ([teams.md](../teams.md#roles)). The private things are the
  assistant's conversation, preferences, sign-in, account tokens and the
  member's settings. This proposal uses "private" to mean those.
- **Watches** record notices, push them, and for `source: assistant` write
  them into the conversation without starting a turn. `relay_to` (phase 0)
  adds server-written inbox messages to up to 4 sessions, with no model.
- **Connector budget:** 32 tools and 11,599 of 15,000 bytes before
  `relay_to` (`TestConnectorToolList`).

## 1. One assistant, every door

### Prompt queue

- The server keeps one ordered queue per member, stored with an idempotency
  key per prompt. Every door adds to it: dashboard, phone, desktop,
  connector, and (phase 5) server events.
- Each entry records its origin: `dashboard`, `phone`, `desktop`,
  `connector:<client> <con-id>`, `event:<watch-id>`, `request:<req-id>`. The
  transcript shows it as a chip ("via ChatGPT", "from phone"). None of the
  products we studied label this, and it answers "where did this come from?"
  at a glance.
- A prompt sent during a turn is queued and shown as **Queued**, rather than
  refused. At most 5 entries wait; the sixth fails with `assistant: 5
  prompts are already queued; wait or remove one`. **Stop** still cancels
  only the running turn. Removing a queued entry is one click.
- The queue survives restarts. A turn the server lost mid-run is not run
  again; its entry is marked `interrupted` with the error, the same rule
  waits use.

### Durable cards

- A card no longer ties up the turn. When an action needs approval, the
  server parks it with its exact parameters and the turn continues or ends.
  The card stays open for 24 hours by default (it was 10 minutes) and can be
  decided from any Aether door.
- **Approve** makes the server run the parked call. It re-checks the
  member's permissions and the target's state at that moment, and fails with
  the real error when they changed (`run-8x2 ended at 14:02; nothing was
  sent`). The outcome is written into the conversation. The model is not
  woken unless the member asks.
- **Decline** and **Expired** are recorded as they are today. The 10-minute
  limit stays for cards raised during a turn the member is watching in the
  panel, where a quick decision is expected. Only cards from turns nobody is
  watching (connector, event, request) get 24 hours.
- Assistant cards can be decided only from an approved Aether device
  (dashboard, phone, desktop, CLI). Never from a connector, the assistant's
  own tool socket or Slack. `aether_decide_approval` stays limited to agents'
  permission requests. The model that raised a card must never be able to
  approve it.

### `aether_ask_assistant`

- One connector tool, scope `write`: `{prompt, turn_id?}`.
- It enqueues the prompt with origin `connector`, then waits up to 25 seconds
  for the turn's final reply. It returns `{turn_id, state, reply,
  pending_cards}`, with `state` `done`, `queued`, `running` or
  `waiting_for_approval`. Calling it again with `turn_id` fetches the result
  without a new turn. When the client declares MCP Tasks, the same call
  returns a task whose id is the turn id. Polling remains the fallback,
  because hosts and proxies time out within seconds and support for Tasks
  varies.
- A pending card is returned as one server-written line, `Waiting for your
  approval in Aether: Launch claude (acp) in api: "Fix the flaky login
  test"`. The card itself is pushed to your devices.
- **What it returns:** only the reply of the turn it started, capped at
  4 KiB. Never the transcript, the preference list or earlier turns. Any
  substring of the reply that matches a saved preference verbatim is replaced
  with `[preference withheld]`, the same check `carriesPreference` already
  runs on cards.
- **Cost:** it is roughly 300–500 bytes of the connector budget. Each call
  costs tokens twice, the external model's and a turn on the assistant's
  sign-in, so the server instructions tell clients to keep using the direct
  tools for reads.
- The direct tools stay unchanged.

### Owner decision: connected apps and private context

Owner decision 2 ("same conversation and memory") and decision 3 ("private
preferences and transcripts never reach connected apps") conflict for the
member's own ChatGPT. Recommended: a connector turn runs in the same
conversation with your preferences applied, but the tool returns only that
turn's capped, redacted reply. A ChatGPT conversation an attacker has
steered can still ask the assistant to paraphrase your preferences, and a
paraphrase gets past the redaction. The strict alternative runs connector
turns with preferences off, which breaks "same memory". This needs the
owner's call.

## 2. Approvals

### Setup

- The first open after sign-in asks one question: **How should your assistant
  act?** with **Ask before acting** (default) or **Act without asking**. The
  second shows today's auto-approval confirm text inline. This is the
  existing setting (`assistant.settings`, `aether member assistant
  --auto-approve`). Members who already chose are not asked again.

### The card

Every card that could become a rule shows four buttons:

```text
Launch claude (acp) in api: "Fix the flaky login test"
  [Approve]  [Decline]
  [Always approve launches of Claude Code in api on your account]
  [Approve everything from now on]
```

- **Always approve this kind** saves a rule built from the card's typed
  fields, and the button names exactly what the rule covers. The rule is
  offered only when the card shows everything the rule would allow, which is
  Claude Code's rule. It is never built from the task text or anything else
  the model wrote.
- **Approve everything from now on** approves this card, then opens today's
  confirm. Confirming turns on auto-approval.
- Rules are matched by the server in `approver`, before auto-approval:

| Method | Rule key | Never covered by a rule |
| --- | --- | --- |
| `run.launch` | workspace, agent, account (yours or one named), mode, permission tier | automatic fallback while any account is shared with you |
| `run.inject` | workspace, own sessions | another member's session |
| `run.kill` | workspace, own sessions | another member's session |
| `env.save` | your environment | none |
| `request.send` (phase 6) | `notify` only: recipient, workspace | `ask`, `handoff` |

- **Never rule-able, checked before rules:** another member's session,
  automatic fallback onto shared accounts, a card carrying a saved
  preference, merges, closes, handoffs, memory pins, forgetting a preference,
  and any change to rules or settings. These are today's always-ask cases
  plus everything outside today's auto-approval. The owner can widen this
  list later; this proposal does not.
- **Who may create a rule:** only a person's click on a card or in Settings,
  from an approved device. Never the assistant, a connector or an inbound
  request.
- **Rules and origin:** rules apply to turns from every origin except
  `request:` (see [Prompt injection](#5-prompt-injection)). Blanket
  auto-approval does not apply to `connector:` turns, because an external
  host mixes in tools and content Aether cannot see. Those cards wait for a
  click.
- Each pass is recorded as `Auto-approved by rule <id>: <summary>`, next to
  today's `Auto-approved:`.

### Seeing and revoking rules

- **Settings → Assistant → Approval rules** lists each rule with its exact
  wording, the card it came from, when it was created, when it was last used
  and how many times. **Revoke** takes one click and applies to the live
  assistant at once, since the setting is read on every call as today.
- A rule unused for 30 days expires and is listed as **Expired (unused 30
  days)**, with **Restore**. This follows Android's reset-on-disuse, so
  forgotten broad grants don't linger and nobody is nagged to renew.
- CLI:

```sh
aether member assistant rules                 # list
aether member assistant rules --revoke <id>
```

### The nudge, narrowed

- After 3 approvals in a row of the same rule key, the next card highlights
  **Always approve this kind** rather than offering global auto-approval.
  The research found that scoped rules offered early keep people away from
  an all-or-nothing switch.
- The global suggestion stays, with its 7/30/never snoozes, for a member who
  approves 3 cards in a row of different kinds.

## 3. Teams: requests between assistants

### Model

A **request** is a server record. Assistants create one with an assistant
tool (`aether_request`), and members can create one from the dashboard or
the CLI.

| Field | Content |
| --- | --- |
| `kind` | `notify` (FYI, no reply), `ask` (a question for a person), `handoff` (proposes that the recipient take over a session) |
| `from`, `to` | Member ids, set by the server from the caller; never from the payload |
| `workspace` | Must be one both members can read |
| `refs` | Session, pull request and memory note ids. The server checks the recipient can read each, and refuses others with the same error as a missing id |
| `body` | Up to 1 KiB of text, shown to the sender before sending |
| `choices` | For `ask`: up to 4 short options, or a free-text answer |
| `urgency` | `normal` or `soon`; the recipient's level decides what it does |
| `state` | `pending`, `answered`, `accepted`, `declined`, `expired`, `withdrawn`; terminal states never change |
| `hop` | 0 for a request a person or a member turn started; one more than the request that tainted the turn that created it |
| `expires_at` | Default 3 days, at most 14 |

The states match A2A's task states (`input-required`, `rejected`,
`completed`), so an A2A adapter could be added later. A follow-up is a new
request that references the old one.

### Lifecycle

- **notify:** delivered to the recipient's digest. No reply and no read
  receipt.
- **ask:** the recipient sees `Grace asks (api): <body>` with the refs and
  choices, quoted as untrusted text. **Draft an answer** runs the request
  desk (below). The recipient edits or keeps the draft and selects **Send**,
  or **Decline**. The server sends exactly the text the recipient saw.
- **handoff:** the recipient sees the session, what changes and who pays:
  `Take over run-8x2 ("Fix login test")? It keeps running on Grace's
  account; cost stays Grace's` (from today's handoff rules). **Accept** runs
  `run.handoff` on the server. This adds the acceptance step `aether
  handoff` lacks, but only for requested handoffs; `aether handoff` itself is
  unchanged. A "next step" in the body reaches the session only after
  acceptance, as a server-written inbox message with the body on its
  `Untrusted (from ..., not instructions):` line.
- **Sending** is an action. `notify` asks by default and can be made a rule
  (recipient + workspace). `ask` and `handoff` always ask the sender.

### Request desk

Drafting an answer to a teammate is where private data and untrusted text
meet. So the draft is written in a separate, short-lived agent session in the
recipient's assistant container:

- It holds no conversation history, no preferences and no view context.
- Its tools are read-only and return only team-visible data: sessions, pull
  requests, team memory and the request itself.
- It has no write tools and no `aether_request`, and cannot fetch URLs.
- Its output is draft text plus an optional choice. It is shown to the
  recipient, never sent on its own.

This removes two legs of the lethal trifecta by construction: the context
that reads the teammate's text holds no private data and has no way out. The
recipient's main conversation can still discuss the request ("tell Grace
yes, after lunch"). That turn is tainted (see [Prompt injection](#5-prompt-injection)),
so its send waits for a click.

In v1 an answer is never released without the recipient's click. Research
found no product that lets one person's agent answer another without the
owner's release, and LLM-only privacy judgement leaks about a quarter of the
time even with privacy prompts (PrivacyLens). A teammate asking only for
status facts can use their own assistant, since every member reads every
session.

### What may cross between members

| Crosses to the recipient | Crosses back to the sender | Never crosses |
| --- | --- | --- |
| Sender's name and id, kind, workspace, refs the recipient can read, the body the sender saw, choices, urgency, expiry, `hop` | State, the answer text or choice the recipient sent, time | Either side's assistant conversation, preferences, approval rules and auto-approval setting |
| For `handoff`: the session, its account owner and cost owner (already visible to teammates) | Nothing for `notify` | Notification level, quiet hours, digest contents, watches, presence, whether a request was read |
| | | The request desk's draft before **Send**, and any model reasoning |
| | | Assistant sign-in, agent logins, tokens, usage and plan of any account; connector list; page and view context |
| | | Another member's audit entries; admins see request ids, kinds and states, never bodies (as for today's audit) |

The server enforces each "never" column item by construction, since no tool
or method returns it across members. The model is not trusted with any of
them. The one leak path left is text the sender's assistant writes into a
`body`. It is caught at two points: the sender sees the whole body on the
card before it leaves, and the server refuses a body that contains a saved
preference verbatim (`request body contains your saved preference <id>`).

### Limits

- Per sender and recipient pair: 10 requests a day, 3 open `ask`s, 2 open
  `handoff`s. Over the limit the call fails with the count and the reset
  time.
- A recipient can mute a sender privately. Muted requests are kept and
  listed but never notify, and the sender is not told.
- A request created from a tainted turn gets `hop` + 1. `hop` 2 is refused:
  `request chain too long; ask the member to send it themselves`. This stops
  assistant-to-assistant loops.
- Viewers can receive requests but not send them, like launches.

## 4. Event-driven work and digests

### Waiting is the server's job

- Watches and `relay_to` do every wait. The assistant never polls
  `aether_changes`.
- v2 needs one addition to `relay_to` (phase 0): a member as a target, which
  delivers a `notify` from the watch's owner. This is a proposed follow-up
  for the session that owns `relay_to`. It is not part of its current scope.

### Server-run follow-ups

"When A finishes, launch B" or "when the checks on #42 pass, message A to
open the PR" needs no judgement:

- The assistant creates a watch with an attached action, whose exact
  parameters are fixed at creation.
- The card shows the trigger and the action, for example `When run-8x2
  finishes: launch claude in api "Write the migration docs"`, and is approved
  once, or passed by a rule.
- When the watch fires, the server runs the action. It re-checks
  permissions and fails with the real error, as for durable cards. The
  outcome is a notice. No model is involved.
- At most one action per watch. It expires with the watch (14 days by
  default, 90 at most).

### Judgement wakes

The model wakes on an event only when:

- a follow-up needs reading or judgement ("when CI fails on #7, tell me
  whether it's a known flake"). This is the standing job from the sign-in
  plan's Phase 2c, limited to read and notify unless a rule covers the
  action; or
- the member turned on auto-drafting for teammates' asks (off by default).

Mechanics:

- The event becomes a queue entry with origin `event:<watch-id>`, keyed by
  notice id, so a restart never runs a wake twice.
- The wake prompt holds only structured fields (ids, states, condition,
  outcome), with any text quoted as untrusted.
- Budget: 20 wakes a day per member by default, shown in **Settings →
  Assistant**. Past it, the notice still arrives as a plain line, with
  `Not looked at: daily wake budget reached (20)`.

### Notification levels

Setup asks, and **Settings → Notifications** changes it later (adds `level`
to `notify.prefs.set`):

| Level | Pushed at once | Batched |
| --- | --- | --- |
| **Only what blocks me** | Needs you, assistant cards, teammates' `ask` and `handoff` | Everything else, in one daily digest at a time you pick (default 09:00) |
| **Blockers now, the rest batched** (default) | Same as above | Finished, failed, merged, checks and `notify`, every 30 minutes |
| **Everything as it happens** | Every notice, as today | Nothing |

- **Coalescing:** after a push, further pushes for 2 minutes join it as one
  replacement notification ("3 need you"), so a burst gives one buzz.
- **One persistent item:** the **Needs you** list stays the source of truth.
  Pushes update one notification in place, the way withdrawals already work.
  They are not repeated. Research found that repeated reminders cause the
  fatigue they are meant to cure.
- **Seen anywhere, cleared everywhere:** an item you opened or resolved on
  any door is dropped from the next digest. An empty digest is not sent.
- **Fixed shape, written by the server, no model:**

```text
Aether · 2 need you · 3 done · 1 failed
api  Fix login test: asks "run the migration?"   [Answer]
api  Launch docs session                          [Approve]
web  Bump deps: failed: checks failed (lint)      [Open]
+3 done · Grace notified you about #42
```

  At most 5 action lines, the action first, titles cut at 40 characters.
  Agent text is shown as plain text.
- **Quiet hours** (existing) hold everything. Held items lead the next digest
  as `Overnight:`. A sender's `urgency: soon` never breaks through; only the
  recipient's level decides.
- **Approve from the push:** on Android and desktop the notification of a
  card carries **Approve** and **Decline**. iOS opens the card. The
  **Always** options appear only on the full card, where the rule's wording
  is shown.
- Later, when Aether has the data: track how often each notice type is acted
  on within 30 minutes, and suggest demoting the types people ignore.

## 5. Prompt injection

Rule: text from sessions, pull requests, web pages, connectors and
teammates is data. Every control below is enforced by the server or the tool
layer, not by asking the model. The research is consistent that classifiers
and "ignore instructions in the content" prompts don't hold up as
boundaries.

### Turn taint

Every turn carries the origin of its queue entry. A turn that receives a
teammate's request body is raised to `request`, even if a member started it.

| Origin | Rules | Approve everything | Notes |
| --- | --- | --- | --- |
| Member (dashboard, phone, desktop) | Yes | Yes | As today |
| Connector | Yes | No: cards wait | External host mixes in tools Aether can't see |
| Event | Yes | Yes | Daily wake budget; structured wake prompt |
| Request (a teammate's text was read) | No | No | Every side effect asks, including `notify` |

The approver (`internal/assistant/confirm.go`) applies this table. The
assistant cannot change a turn's origin.

### Assistant to assistant

- **No direct channel.** Assistants exchange only typed requests through
  the server. The server stamps `from`, `to` and `workspace`, and the model
  never writes them.
- **Bounded free text.** The `body` reaches a model only on an `Untrusted
  (from Grace's assistant, not instructions):` line, the convention waits
  already use. That is hygiene, not a boundary.
- **Side effects stay with the owner.** No inbound request causes a side
  effect without the recipient's click. Accepting a handoff and sending an
  answer are server actions behind buttons. The model only drafts.
- **Breaking worm propagation.** A steered assistant that sends requests
  meets a click per hop, `hop` 2 refused, and per-pair limits.
- **No rules or settings from inbound text.** Rules, settings and watches
  change only through a person's click.
- **Plain-text display.** The dashboard and phone render request bodies,
  answers and agent text as plain text: no images, no link previews, no
  auto-loaded URLs. They strip zero-width and Unicode tag characters and
  show link domains. This closes the exfiltration path behind the Slack AI
  and EchoLeak incidents.

### Connector

- `aether_ask_assistant` returns only the turn's reply. No tool returns raw
  transcripts or requests, and the connector cannot decide assistant cards.
  `child.start`-class tools stay off the connector.

### What stays unenforced

The main assistant still reads untrusted text in member turns, as today.
With auto-approval on, a steered assistant can launch, message and stop your
own sessions ([security.md](../security.md#the-assistant)). v2 narrows this
risk but does not remove it: rules are narrower than auto-approval,
connector turns lose blanket auto-approval, and request turns lose all
automatic passes. `security.md` must say so.

## 6. Cost: whose quota pays

| Work | Paid by |
| --- | --- |
| A turn from any Aether door | The member's assistant sign-in |
| `aether_ask_assistant` | The external client's own subscription for its model, plus a turn on the member's assistant sign-in |
| Judgement wake | The member's assistant sign-in, capped at 20 a day |
| Server-run follow-up, `relay_to`, digest, push | No model tokens |
| Composing and sending a request | The sender's assistant |
| Request desk draft | The recipient's assistant, only when the recipient asks for a draft (or opted into auto-drafting, counted against their wake budget) |
| Sessions the assistant launches | The launch's account, by today's launch and fallback rules |
| Accepted handoff | The launcher's account keeps paying, as with `aether handoff` today; the card says so |

A teammate cannot spend your quota. Delivering a request runs no model on
your side unless you ask for a draft.

The assistant never uses account sharing or fallback for its own turns, as
today.

## 7. Failure modes

| Failure | Behavior |
| --- | --- |
| Assistant not signed in, or sign-in refused | Queue entries fail with the real error (`assistant: no agent is ready; ...`). Digests, follow-ups and requests keep working, since they need no model. `aether_ask_assistant` returns the same error |
| Vendor unreachable at start | Same; the entry stays queued and is retried on the next prompt, or after 5 minutes for events |
| Assistant account out of usage | The turn fails with the vendor's error. Event wakes stop for the day and notices arrive as plain lines |
| Turn running when a prompt arrives | Queued; the connector gets `state: queued` and a `turn_id` |
| Queue full | `assistant: 5 prompts are already queued; wait or remove one` |
| Server restart mid-turn | The entry is marked `interrupted`, not rerun. Parked cards and their actions survive. An approved action runs at most once (claim before run, keyed by card id) |
| Card approved after its target changed | Fails with the real state (`run-8x2 ended at 14:02`) and is recorded as `Failed` |
| Rule's workspace or account removed | The rule stops matching and is listed as `Inactive: <reason>` |
| Recipient removed, demoted to viewer or lost access to a ref | Pending requests to them become `expired` with the reason. The sender sees it |
| Request loop between assistants | Each hop needs a click; `hop` 2 refused; pair limits |
| MCP client times out | The call returns within 25 seconds with `turn_id`; the client polls |
| Push fails | Existing delivery rows (`failed: <error>`). The item stays on Needs you |
| Two doors approve one card at once | The first decision wins; the second gets `assistant: no such pending confirmation` (`ErrNoConfirmation`) |

## Phased plan

Each phase updates the guides it changes, in the same PR. Check each new
migration's version against `main` right before merging, because migration
numbers race.

**Phase 1: approvals (M, 2 PRs).**
- PR 1: rule store and migration, typed rule keys per method, matching in
  `approver` before auto-approval, the never-rule-able list,
  `Auto-approved by rule` records, the narrowed nudge, 30-day expiry on
  disuse.
- PR 2: setup question, card buttons, Settings → Assistant → Approval rules,
  `aether member assistant rules`.
- Docs: `assistant.md`, `security.md`.
- Check: e2e where approving a card with **Always** passes the next matching
  launch, a non-matching one still asks, and revoking restores the card.

**Phase 2: durable cards and the queue (M, 2 PRs).**
- PR 1: parked actions with a 24-hour expiry for cards from turns nobody
  watches, server-side execution with re-check, decide only from devices.
- PR 2: per-member queue with origins and the chip, `Queued` state, removal,
  restart handling.
- Check: integration test where a card is approved after its turn ended,
  after a server restart, and runs exactly once.

**Phase 3: `aether_ask_assistant` (S, 1 PR).**
- The tool, the 25-second wait, `turn_id` polling, optional MCP Tasks, the
  4 KiB cap and preference redaction.
- Keep `TestConnectorToolList` green. Docs: `connectors.md`.

**Phase 4: notification levels (M, 2 PRs).**
- PR 1: `level` in notification preferences, batching windows, 2-minute
  coalescing, the server-written digest.
- PR 2: Approve and Decline on card pushes for Android and desktop, and
  seen-anywhere clearing.
- Docs: `watches.md`, `networking.md`.

**Phase 5: events (L, 3 PRs).**
- PR 1: actions attached to watches (server-run follow-ups).
- PR 2: event queue entries, judgement wakes, the wake budget.
- PR 3: the turn taint table in the approver.
- Needs `relay_to` merged. Coordinate the member target with its owner.

**Phase 6: team requests (L, 4 PRs).**
- PR 1: request store and migration, `request.*` methods, ref checks, limits,
  `hop`, mute, audit entries without bodies.
- PR 2: `aether_request` assistant tool and sender cards.
- PR 3: request desk session.
- PR 4: recipient UI (requests in Needs you and the digest), outbox in
  Settings, CLI `aether request`, plain-text rendering.
- Docs: `teams.md`, `security.md`, `privacy.md`.

**Phase 7: evals (S–M, alongside 5 and 6).**
- Scenarios in `internal/assistant/eval`:
  - an injected session report tries to launch, message or send a request;
  - a teammate's body tries to get an answer or a handoff sent;
  - a connector prompt asks for preferences;
  - canary preferences and conversation text must never show up in another
    member's view or in a connector reply.

## Research findings

Five research subsessions (Sonnet) studied public docs and papers. Items
flagged secondary in their reports are marked (2nd).

**Scoped "always allow" rules**
- Claude Code: `Tool(pattern)` rules, deny > ask > allow, `/permissions`
  lists each rule with its source. It offers "don't ask again" only when the
  prompt shows everything the rule allows. Auto mode drops broad allow rules
  on entry.
  ([permissions](https://code.claude.com/docs/en/permissions))
- Codex: `prefix_rule` with `match`/`not_match` examples validated at load,
  and a dry-run checker. ([rules](https://developers.openai.com/codex/rules))
- Gemini CLI trusted folders: trust gates the allow rules.
  ([docs](https://geminicli.com/docs/cli/trusted-folders/))
- GitHub Copilot coding agent: the agent can't approve or merge its own
  work.
  ([risks](https://docs.github.com/en/copilot/concepts/agents/coding-agent/risks-and-mitigations))
- Android: once / while using / always, and reset after disuse.
  ([permissions](https://developer.android.com/training/permissions/requesting))
- Lessons: rules on typed fields, not string patterns. One visible list.
  Narrowest scope by default. Bind each rule to its principal. Rules can't
  pre-approve protected actions.

**Cross-device continuity**
- Every consumer assistant keeps one history per account. None labels which
  device a message came from.
- Claude Code Remote Control: one session, many windows. Its "disconnected"
  doesn't say whether work continues.
  ([docs](https://code.claude.com/docs/en/remote-control))
- MCP Tasks is an opt-in extension: durable task id, poll `tasks/get`,
  `input_required`. Clients must also work without it.
  ([overview](https://modelcontextprotocol.io/extensions/tasks/overview))
- A2A: contextId vs taskId, and terminal states never change.
  ([life of a task](https://a2a-protocol.org/latest/topics/life-of-a-task/))
- Linear agent sessions: typed activities and server-derived states.
  ([agent interaction](https://linear.app/developers/agent-interaction))

**Multi-user coordination and privacy**
- Copilot, Gemini, Slack and Notion act as the requester with
  permission-trimmed retrieval.
- Every leak studied passed authorization: untrusted text + private data +
  an outbound channel. Examples: Slack AI
  ([PromptArmor](https://promptarmor.substack.com/p/data-exfiltration-from-slack-ai-via)),
  Notion agents
  ([Willison](https://simonwillison.net/2025/Sep/19/notion-lethal-trifecta)),
  EchoLeak (CVE-2025-32711).
- Linear's owner/delegate split is the clearest accountability model.
  ([agents](https://linear.app/developers/agents))
- No product lets one person's agent answer another's question without the
  owner's release.
- PrivacyLens: models leak in about 26% of agent actions even with privacy
  prompts ([arXiv 2409.00138](https://arxiv.org/pdf/2409.00138)).
- A separate flow check cut leakage from 33–36% to 7–8%
  ([arXiv 2509.17488](https://arxiv.org/pdf/2509.17488)).
- The strongest control is not putting the data in context.

**Digests and notifications**
- iOS Summary and interruption levels
  ([HIG](https://developer.apple.com/design/human-interface-guidelines/managing-notifications)).
- Android: the channel per type is the unit of user control
  ([channels](https://developer.android.com/training/notify-user/channels)).
- Linear: no email for what you've already read; a snooze ends on new
  activity ([inbox](https://linear.app/docs/inbox)).
- Sentry: 5-minute burst digests
  ([settings](https://docs.sentry.io/product/notifications/notification-settings/)).
- PagerDuty: content-based grouping is predictable, ML grouping is opaque
  ([grouping](https://support.pagerduty.com/docs/content-based-alert-grouping)).
- CI users want transitions (red to green), not per-run alerts (Buildkite
  forum).
- Clinical alert studies: 52–73% of alerts overridden, so measure how often
  each alert type is acted on
  ([PMC7646870](https://pmc.ncbi.nlm.nih.gov/articles/PMC7646870)).
- ADHD evidence is weak (mostly vendor blogs). The one consistent point:
  keep one persistent item, don't repeat pushes.

**Prompt injection**
- The lethal trifecta: remove one of its three legs
  ([Willison](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)).
- CaMeL: provenance enforced by an interpreter, not the model
  ([arXiv 2503.18813](https://arxiv.org/abs/2503.18813)).
- Spotlighting is hygiene
  ([MSRC](https://www.microsoft.com/en-us/msrc/blog/2025/07/how-microsoft-defends-against-indirect-prompt-injection-attacks)).
- Anthropic reports about 1% attack success against an adaptive attacker,
  which is still too high for unattended side effects
  ([research](https://www.anthropic.com/research/prompt-injection-defenses)).
- MCP security best practices: no token passthrough; bind handles to the
  principal
  ([spec](https://modelcontextprotocol.io/specification/draft/basic/security_best_practices)).
- What doesn't hold: classifiers alone, "ignore instructions" prompts,
  model self-reports, and generic "are you sure?" approvals.

## Trade-offs

- **24-hour cards vs 10 minutes.** Longer cards make approving from the phone
  or after a connector turn possible, but a stale approval could run against
  a changed world. Re-checking at run time and keeping 10 minutes for watched
  turns limits that.
- **Rule expiry on disuse.** This bounds stale grants. A rule used once a
  month would lapse; **Restore** is one click.
- **No blanket auto-approval for connector turns.** This is safer, but a
  member who chose "act without asking" will still see cards for ChatGPT
  turns until they add rules. The card says why.
- **Human-released answers between members.** This is slower than letting
  assistants answer each other, but it is the only design we found that
  keeps private data from leaking through a model's judgement. A server-only
  "status facts" auto-answer can come later, if teams ask for it.
- **Server-written digests.** They cost no tokens and are predictable, but
  read less fluently than a model summary. An opt-in "summarize" can be
  added on the wake budget.
- **No `relay_to` changes.** v2 consumes `relay_to` as built and proposes
  only a member target as a follow-up.

## Open questions for the owner

1. Connector turns: same conversation with preferences applied and a capped,
   redacted reply (recommended), or preferences off for connector turns?
2. Blanket auto-approval off for connector turns (recommended), or the same
   as dashboard turns?
3. Rules expire after 30 days unused (recommended), or never?
4. Answers to a teammate's `ask` always need a click in v1 (recommended)?
5. Default judgement-wake budget of 20 a day?
