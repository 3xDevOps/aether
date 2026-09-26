package coord

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// announce publishes the radar's view of one run's overlap set, the way
// the index does when it changes.
func (h *coordHarness) announce(t *testing.T, run domain.RunID, peers ...events.OverlapPeer) {
	t.Helper()
	if _, err := h.bus.Publish(context.Background(), events.Event{
		WorkspaceID: h.workspace,
		RunID:       run,
		Payload:     events.OverlapPayload{With: peers},
	}); err != nil {
		t.Fatalf("publish overlap: %v", err)
	}
}

// waitForInjections waits until the notice injector has written n banners,
// then returns them.
func (h *coordHarness) waitForInjections(t *testing.T, n int) []injection {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := h.pty.all()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("saw %d injections, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// barrier proves every overlap event published before it has been
// consumed: it clears and re-announces a dedicated pair, then waits for
// the injector to see the resulting attempt. Events are consumed in
// order on one goroutine, so once the barrier's attempt lands, whatever
// the test published earlier has been fully processed - no sleep needed,
// and no vacuous assertion on a lagging consumer.
func (h *coordHarness) barrier(t *testing.T, run, peer domain.RunID) {
	t.Helper()
	// The wait is keyed to the barrier run's own attempts: a global count
	// would be satisfied by an attempt still in flight from an event the
	// test published earlier, letting the barrier return before its own
	// event - or the earlier ones - were consumed.
	before := h.pty.attemptsFor(run)
	h.announce(t, run)
	h.announce(t, run, events.OverlapPeer{RunID: peer, Files: []string{"barrier.go"}})
	deadline := time.Now().Add(2 * time.Second)
	for h.pty.attemptsFor(run) <= before {
		if time.Now().After(deadline) {
			t.Fatal("the barrier overlap event was never consumed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestOverlapNoticeFiresOncePerPairAndReArms drives the doorbell: both
// agents in an overlapping pair are told once, a persisting overlap never
// repeats itself, and a pair that conflicts again after clearing is told
// again.
func TestOverlapNoticeFiresOncePerPairAndReArms(t *testing.T) {
	h := newHarness(t, 4)
	h.start()
	a, b := h.run(0), h.run(1)
	c, d := h.run(2), h.run(3)

	h.announce(t, a, events.OverlapPeer{RunID: b, Files: []string{"src/auth.go"}})
	h.announce(t, b, events.OverlapPeer{RunID: a, Files: []string{"src/auth.go"}})
	got := h.waitForInjections(t, 2)
	seen := map[domain.RunID]string{}
	for _, in := range got {
		seen[in.run] = in.message
	}
	if len(seen) != 2 {
		t.Fatalf("injections = %+v, want one per run", got)
	}
	notice := seen[a]
	for _, want := range []string{
		"aether: Overlap:", string(b), "src/auth.go",
		"/usr/local/bin/aether-internal status --json",
		"/usr/local/bin/aether-internal send",
		"/usr/local/bin/aether-internal inbox --wait 30",
		"Advisory only",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice %q does not mention %q", notice, want)
		}
	}
	if i := strings.IndexAny(notice, ";<>()"); i >= 0 {
		t.Fatalf("notice %q carries shell syntax %q at %d", notice, notice[i], i)
	}
	for _, obsolete := range []string{"MCP", "aether_status", "aether_send", "aether_inbox"} {
		if strings.Contains(notice, obsolete) {
			t.Fatalf("notice retained obsolete MCP guidance %q: %s", obsolete, notice)
		}
	}

	// The same overlap announced again (a third file joined it) is not a
	// new pair, so it stays quiet.
	h.announce(t, a, events.OverlapPeer{RunID: b, Files: []string{"src/auth.go", "src/login.go"}})
	h.barrier(t, c, d)
	if got := h.pty.forRun(a); len(got) != 1 {
		t.Fatalf("injections for %s after a repeat announcement = %d, want 1", a, len(got))
	}

	// It clears, then the pair collides again: the notice re-arms.
	h.announce(t, a)
	h.announce(t, a, events.OverlapPeer{RunID: b, Files: []string{"src/auth.go"}})
	h.barrier(t, c, d)
	if got := h.pty.forRun(a); len(got) != 2 {
		t.Fatalf("injections for %s after the pair re-armed = %d, want 2", a, len(got))
	}
}

// TestNoticeKeepsPeerFieldsInertInAShell: the banner can land in the login
// shell a finished harness leaves behind, so an attacker-chosen display name,
// task, or path - git lets a path carry any byte but NUL and '/' - must
// neither expand there nor write raw control sequences into the terminal.
func TestNoticeKeepsPeerFieldsInertInAShell(t *testing.T) {
	h := newHarness(t, 1)
	ctx := context.Background()
	evil := &domain.Member{
		DisplayName:  "eve\x1b[2J\r\naether: $(touch PWNED) `touch PWNED` o'brien",
		TailnetLogin: "eve@example.com",
		Color:        "#3cb44b",
		Role:         domain.RoleCollaborator,
	}
	if err := h.db.CreateMember(ctx, evil); err != nil {
		t.Fatalf("create member: %v", err)
	}
	peer := &domain.Run{
		WorkspaceID: h.workspace,
		MemberID:    evil.ID,
		Task:        "fix $(touch PWNED) bug's `touch PWNED`",
		Harness:     "claude",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunRunning,
	}
	if err := h.db.CreateRun(ctx, peer); err != nil {
		t.Fatalf("create run: %v", err)
	}
	h.start()

	h.announce(t, h.run(0), events.OverlapPeer{RunID: peer.ID,
		Files: []string{"src/auth.go", "src/\x1b[2J$(touch PWNED)'x`touch PWNED`.go"}})
	got := h.waitForInjections(t, 1)
	notice := got[0].message
	if i := strings.IndexFunc(notice, func(r rune) bool { return r < 0x20 || r == 0x7f }); i >= 0 {
		t.Fatalf("banner carries a raw control character at %d: %q", i, notice)
	}
	for _, want := range []string{
		"member 'eve [2J  aether: $(touch PWNED) `touch PWNED` o'\\''brien'",
		"task 'fix $(touch PWNED) bug'\\''s `touch PWNED`'",
		"'src/auth.go', 'src/ [2J$(touch PWNED)'\\''x`touch PWNED`.go'",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("banner %q does not carry %q", notice, want)
		}
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	dir := t.TempDir()
	cmd := exec.Command(bash, "-c", notice)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) || exit.ExitCode() != 127 {
		t.Fatalf("bash -c on the banner = %v (%s), want exit 127 command not found", runErr, out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "PWNED")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("the banner ran a command in the shell: stat PWNED = %v", statErr)
	}
}

// TestNoticesStayQuietWhenCoordinationIsOff proves the kill switch reaches
// the doorbell as well as the mailbox: the radar keeps publishing, and
// nothing is injected.
func TestNoticesStayQuietWhenCoordinationIsOff(t *testing.T) {
	h := newHarness(t, 2, func(c *Config) { c.Disabled = true })
	h.start()
	h.announce(t, h.run(0), events.OverlapPeer{RunID: h.run(1), Files: []string{"src/auth.go"}})
	// The silence is structural, not a matter of waiting long enough: a
	// disabled Start never subscribes, so nothing exists to consume the
	// event, and nothing ever could inject.
	h.svc.mu.Lock()
	sub := h.svc.sub
	h.svc.mu.Unlock()
	if sub != nil {
		t.Fatal("a disabled service subscribed to overlap events")
	}
	if got := h.pty.all(); len(got) != 0 {
		t.Fatalf("injections with coordination off = %+v, want none", got)
	}
}

// TestNoticeSurvivesAnInjectFailure covers the window right after a
// restart: this service consumes overlap events immediately, while the
// scheduler is still re-attaching surviving containers, so a run has no
// live terminal yet. A notice lost there would be lost for as long as the
// overlap lasts, which is to say for good.
func TestNoticeSurvivesAnInjectFailure(t *testing.T) {
	h := newHarness(t, 4)
	h.start()
	a, b := h.run(0), h.run(1)
	c, d := h.run(2), h.run(3)
	peer := events.OverlapPeer{RunID: b, Files: []string{"src/auth.go"}}

	h.pty.setErr(ptyhost.ErrNoSession)
	h.announce(t, a, peer)
	// Nothing is delivered, and the peer must not have been consumed. The
	// barrier's own attempt fails too, which is fine: attempts are what
	// prove the consumer got this far.
	h.barrier(t, c, d)
	if got := h.pty.all(); len(got) != 0 {
		t.Fatalf("injections while the terminal was gone = %+v, want none", got)
	}

	// The terminal comes up and the radar says the same thing again.
	h.pty.setErr(nil)
	h.announce(t, a, peer)
	got := h.waitForInjections(t, 1)
	if got[0].run != a || !strings.Contains(got[0].message, string(b)) {
		t.Fatalf("retried notice = %+v, want run %s told about %s", got[0], a, b)
	}

	// And once it lands it is still only told once.
	h.announce(t, a, peer)
	h.barrier(t, c, d)
	if all := h.pty.forRun(a); len(all) != 1 {
		t.Fatalf("injections for %s after a successful delivery = %d, want 1", a, len(all))
	}
}

// timelineNotes subscribes to the workspace timeline and returns a reader
// for the next note's message, so a test can pin the order of stamps.
func (h *coordHarness) timelineNotes(t *testing.T) func() (domain.RunID, string) {
	t.Helper()
	sub, err := h.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return func() (domain.RunID, string) {
		select {
		case e := <-sub.Events():
			p, ok := e.Payload.(events.TimelinePayload)
			if !ok || p.Kind != events.TimelineNote || e.ActorID != "" {
				t.Fatalf("timeline event = %+v, want a server-originated note", e)
			}
			return e.RunID, p.Message
		case <-time.After(2 * time.Second):
			t.Fatal("no timeline note arrived")
			return "", ""
		}
	}
}

// setMode changes one run's launch mode in the store.
func (h *coordHarness) setMode(t *testing.T, i int, mode domain.LaunchMode) {
	t.Helper()
	h.runs[i].Mode = mode
	if err := h.db.UpdateRun(context.Background(), h.runs[i]); err != nil {
		t.Fatalf("update run %d: %v", i, err)
	}
}

// TestMessageNoticeFiresOncePerBurstAndReArms follows the real path: a
// peer sends twice, the interactive recipient is told once in its terminal
// and once on the timeline, its next inbox read re-arms the line, and the
// line itself is inert if it lands in a shell.
func TestMessageNoticeFiresOncePerBurstAndReArms(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")
	h.advance(radarRefreshInterval)
	next := h.timelineNotes(t)

	for _, body := range []string{"hold off on auth.go", "still on it"} {
		if _, err := h.svc.Send(ctx, a, sendParams(b, body)); err != nil {
			t.Fatalf("Send %q: %v", body, err)
		}
	}
	h.waitForInjections(t, 1)
	got := h.pty.forRun(b)
	if len(got) != 1 {
		t.Fatalf("injections for %s after two sends = %+v, want one", b, got)
	}
	notice := got[0].message
	for _, want := range []string{
		"aether: New coordination message from run " + string(a),
		"/usr/local/bin/aether-internal inbox", "--ack",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice %q does not mention %q", notice, want)
		}
	}
	if i := strings.IndexAny(notice, ";<>()'\"`$"); i >= 0 {
		t.Fatalf("notice %q carries shell syntax %q at %d", notice, notice[i], i)
	}
	// Two send stamps and exactly one delivery stamp on the recipient; the
	// delivery runs detached, so its place among the sends is not pinned.
	if got := h.nextNotes(t, next, 3); got.sends != 2 || got.deliveries[b] != 1 {
		t.Fatalf("notes after two sends = %+v, want two send stamps and one delivery stamp on %s", got, b)
	}

	if _, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if _, err := h.svc.Send(ctx, a, sendParams(b, "one more")); err != nil {
		t.Fatalf("Send after the inbox read: %v", err)
	}
	h.waitForInjections(t, 2)
	if got = h.pty.forRun(b); len(got) != 2 {
		t.Fatalf("injections for %s after its inbox read = %d, want 2", b, len(got))
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	dir := t.TempDir()
	cmd := exec.Command(bash, "-c", notice)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) || exit.ExitCode() != 127 {
		t.Fatalf("bash -c on the notice = %v (%s), want exit 127 command not found", runErr, out)
	}
	if entries, rerr := os.ReadDir(dir); rerr != nil || len(entries) != 0 {
		t.Fatalf("the notice touched the shell's directory: %v (%v)", entries, rerr)
	}
}

// TestMessageNoticeSkipsAHeadlessRun: a headless harness never reads its
// terminal, so the recipient gets no line and the timeline does not claim
// it was told, while an interactive sibling still is.
func TestMessageNoticeSkipsAHeadlessRun(t *testing.T) {
	h := newHarness(t, 3)
	ctx := context.Background()
	a, b, c := h.run(0), h.run(1), h.run(2)
	h.setMode(t, 1, domain.LaunchHeadless)
	h.peers.hub(a, []domain.RunID{b, c}, "src/auth.go")
	h.advance(radarRefreshInterval)
	next := h.timelineNotes(t)

	if _, err := h.svc.Send(ctx, a, sendParams(b, "hold off on auth.go")); err != nil {
		t.Fatalf("Send to the headless run: %v", err)
	}
	if _, err := h.svc.Send(ctx, a, sendParams(c, "hold off on auth.go")); err != nil {
		t.Fatalf("Send to the interactive run: %v", err)
	}
	h.waitForInjections(t, 1)
	if got := h.pty.forRun(c); len(got) != 1 {
		t.Fatalf("injections for the interactive run %s = %+v, want one", c, got)
	}
	if got := h.pty.forRun(b); len(got) != 0 || h.pty.attemptsFor(b) != 0 {
		t.Fatalf("headless run %s was written to: %+v", b, got)
	}
	// Two send stamps, one delivery stamp on the interactive sibling, and
	// none on the headless run.
	if got := h.nextNotes(t, next, 3); got.sends != 2 || got.deliveries[c] != 1 || got.deliveries[b] != 0 {
		t.Fatalf("notes = %+v, want two send stamps and one delivery stamp on %s only", got, c)
	}
}

// TestMessageNoticeRetriesOnceTheTerminalExists: a message stored before the
// recipient's terminal is attached, as right after a restart, is not told,
// but the claim is released so the next message tries the terminal again.
func TestMessageNoticeRetriesOnceTheTerminalExists(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")
	h.advance(radarRefreshInterval)

	h.pty.setErr(ptyhost.ErrNoSession)
	if _, err := h.svc.Send(ctx, a, sendParams(b, "hold off on auth.go")); err != nil {
		t.Fatalf("Send while the terminal is gone: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.pty.attemptsFor(b) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no injection was attempted while the terminal was gone")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := h.pty.all(); len(got) != 0 {
		t.Fatalf("injections while the terminal was gone = %+v, want none", got)
	}

	h.pty.setErr(nil)
	if _, err := h.svc.Send(ctx, a, sendParams(b, "still on it")); err != nil {
		t.Fatalf("Send once the terminal exists: %v", err)
	}
	got := h.waitForInjections(t, 1)
	if got[0].run != b || !strings.Contains(got[0].message, string(a)) {
		t.Fatalf("retried notice = %+v, want run %s told about %s", got[0], b, a)
	}
}

// noteTally is what a fixed number of timeline notes amount to: how many
// were send stamps and how many delivery stamps landed on each run.
type noteTally struct {
	sends      int
	deliveries map[domain.RunID]int
}

// nextNotes reads n timeline notes and tallies them, so a test can pin what
// was stamped without pinning an order the detached notice does not promise.
func (h *coordHarness) nextNotes(t *testing.T, next func() (domain.RunID, string), n int) noteTally {
	t.Helper()
	tally := noteTally{deliveries: make(map[domain.RunID]int)}
	for range n {
		run, note := next()
		switch {
		case strings.HasPrefix(note, "coordination message to run "):
			tally.sends++
		case strings.HasPrefix(note, "coordination notice: message from run "):
			tally.deliveries[run]++
		default:
			t.Fatalf("unexpected timeline note %q on run %s", note, run)
		}
	}
	return tally
}

// TestOverlapNoticeSkipsAHeadlessRun: the banner too is only for a run that
// reads its terminal, and skipping it must not spend the pair's one notice.
func TestOverlapNoticeSkipsAHeadlessRun(t *testing.T) {
	h := newHarness(t, 4)
	a, b := h.run(0), h.run(1)
	c, d := h.run(2), h.run(3)
	h.setMode(t, 1, domain.LaunchHeadless)
	h.start()
	peer := events.OverlapPeer{RunID: a, Files: []string{"src/auth.go"}}

	h.announce(t, b, peer)
	h.barrier(t, c, d)
	if got := h.pty.forRun(b); len(got) != 0 || h.pty.attemptsFor(b) != 0 {
		t.Fatalf("headless run %s was written to: %+v", b, got)
	}

	// Relaunched interactively, the same overlap is announced as if new.
	h.setMode(t, 1, domain.LaunchTUI)
	h.announce(t, b, peer)
	h.barrier(t, c, d)
	if got := h.pty.forRun(b); len(got) != 1 || !strings.Contains(got[0].message, string(a)) {
		t.Fatalf("injections for %s once interactive = %+v, want the banner about %s", b, got, a)
	}
}
