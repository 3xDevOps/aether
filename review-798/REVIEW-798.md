# Adversarial review of PR #798 (sticky session notes)

Reviewed head `0830a9651` of `aether/run-task-sticky-note-comments-with-c-bf1dzyp9mv`.
The server half (permissions, idempotency, paging, migration) is sound; the
confirmed bugs are in the dashboard.

## Findings, most severe first

1. **CONFIRMED: completed and deleted notes still show in the Chat transcript; a deleted note comes back.**
   `web/src/store/sessions.ts:537` (`rowsForRun`, untouched by the PR) turns every
   `comment` into an inline `note` row without checking `resolution`. Every open
   note shows twice in Chat (transcript row and sticky), and a completed note
   stays in the transcript after its scratch-off. On delete, the server publishes
   the `deleted` event before it answers the RPC: `sync.ts` drops the message,
   then `room.ts:230` `confirm` calls `upsertRoomMessage(result.message)`, which
   re-inserts it, so the deleted text reappears as a transcript row until reload.
   The e2e spec scopes its assertions to the rail, so it misses this.
   Repro: `resolved-comments-in-transcript.repro.test.ts` (both tests fail).

2. **CONFIRMED: a "Complete" refused during the scratch animation leaves the card invisible with its error inside.**
   `web/src/routes/run/session-notes.tsx:125-162`. The error effect cancels only
   `el.getAnimations()` on the `<li>`; the scratch strokes animate child `<path>`s
   and keep running. When they finish, `runExit` plays the fade and height
   collapse with `fill: 'forwards'`, ending at opacity 0 and height 0. The
   scratch takes about 450-570ms, longer than a typical refusal (conflict,
   permission, network), so this is the common case for a refused completion.
   Repro: `sticky-scratch-refusal.repro.test.tsx`, first test.

3. **CONFIRMED (code reading): two dashboards resolving the same note leave the loser showing it open.**
   `web/src/routes/run/room.ts:231`: `revert` always sets the resolution to `''`
   instead of its previous value. A marks a note done while B deletes it; A wins.
   B gets `-32003`. B may already hold `done` from A's event, but the revert
   overwrites it with open: a live sticky with a conflict error whose Retry
   conflicts forever, until the next reload.

4. **CONFIRMED (code reading): with the browser docked, the rail covers the chat column.**
   `web/src/routes/run/index.tsx:101`: `notesWide = frame.width >= 1248`, but the
   frame also holds `BrowserDock` (line 338). A 1600px frame with the browser
   docked leaves a ~800px session pane, `notesWide` stays true, and the 236px
   rail is drawn over the 736px column. The docs say the switch follows the
   pane width; the code measures the frame.

5. **CONFIRMED: a refused delete announces "Note deleted." to screen readers.**
   `session-notes.tsx:230-233`. When the error effect cancels the exit animation,
   `.finished` rejects, the `catch` swallows it, and `onSettled` still sets the
   announcement. Repro: `sticky-scratch-refusal.repro.test.tsx`, second test.

6. **CONFIRMED (code reading): the last note's announcement is never read.**
   `session-notes.tsx:243` and the strip's equivalent. `onSettled` empties `ids`
   and sets the announcement in the same render; the rail returns `null` at
   count 0, unmounting its `role="status"` region. That `<span>` is also a
   direct child of `<ol>`, which is invalid.

7. **CONFIRMED (code reading): Activity logs "Bob added a note" again when anyone completes or deletes Bob's note.**
   `Resolve` publishes a persisted `workspace.room_message` event
   (`internal/collab/events.go:42`) with `ActorID` set to the note's author and
   kind `comment`. Activity includes the type (`web/src/routes/activity/filter.tsx:18`)
   and `web/src/components/event-words.tsx:80` `roomLine` ignores `resolution`,
   so it renders "added a note" under the author. The resolver is never named.

8. **PLAUSIBLE: the gateway docs overstate "deleted".**
   `docs/local-gateway.md` says a deleted comment "is excluded there and
   everywhere else". `GetRoomMessage` still returns it, so its image attachments
   still download through `openRoomImage` (`internal/sshd/development_stream.go:288`),
   and finding 1 contradicts the sentence too.

9. **PLAUSIBLE, low: two edge cases.**
   On a wide screen in Terminal or Changes, open comments show nowhere: Details
   hides them and the rail exists only in Chat. This matches the spec's wording,
   so it is a design gap rather than a bug. After a member merge, the survivor
   cannot resolve notes written under the merged-away ID: the server
   (`internal/collab/moderation.go`, `by != msg.ActorID`) and `canResolveNote`
   both compare the raw `actor_id`.

## Checked and sound

- `run.room.resolve` uses the View guard on the message's run (pending members
  and non-members are refused); `Resolve` requires a `comment` (`-32602`
  otherwise, so `steer_request`, `question` and `reply` are refused) and allows
  only the author, the run's owner or an admin; the scope check confirms the
  run belongs to the message's workspace.
- The store's conditional update (`WHERE resolution = ''`) replays the same
  outcome and returns `-32003` for a different one.
- `run.room.list` filters deleted rows in SQL and still pages by
  `(created_at, rowid)`.
- Queued-steer counts, open requests (questions and steers only), the attention
  count, evidence capture and the merge rules (`resolved_by` classed like
  `decided_by`) are unaffected.
- The v108 migration (`ADD COLUMN ... NOT NULL DEFAULT ''` with a CHECK) is safe
  on a non-empty table and does not collide with `main`.
- Double clicks are guarded by `started`.

## What was run

- `go test ./internal/store/ ./internal/collab/ ./internal/sshd/ ./internal/protocol/`: pass.
- `vitest` on the PR's `session-notes.test.tsx`: 7/7 pass.
- The four repro tests in this directory, copied into `web/src/routes/run/` on
  the PR branch: all four fail on assertions, as described. They assert the
  correct behavior, so they pass once the bugs are fixed. They live outside
  `web/src` because they import `session-notes.tsx`, which exists only on the
  PR branch.

Not run: the Playwright e2e suite, `make lint`, `make vet`, a visual check in a browser.
