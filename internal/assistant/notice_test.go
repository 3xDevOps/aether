package assistant

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/watch"
)

func newNoticeService(t *testing.T) (*Service, events.Subscription) {
	t.Helper()
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return New(Config{Dir: t.TempDir(), Store: newMemStore(), Bus: bus}), sub
}

func runNotice(condition, key, body string) watch.AssistantNotice {
	return watch.AssistantNotice{
		WatchID: "wat-1", WorkspaceID: "ws1", TargetKind: protocol.WatchTargetRun, RunID: "run-1",
		Condition: condition, Key: key, Title: "Fix login", Body: body, Link: "/?run=run-1", WatchLink: watch.WatchLink("wat-1"),
	}
}

func TestTranscribeWordsEachNoticeAndLinksItsWatch(t *testing.T) {
	t.Parallel()
	svc, sub := newNoticeService(t)
	for _, tc := range []struct {
		notice      watch.AssistantNotice
		kind, title string
	}{
		{runNotice(protocol.WatchNeedsYou, "input:s|question|q1", "Question: Which region?"), events.AssistantNoticeNeedsInput, `Session "Fix login" needs your input`},
		{runNotice(protocol.WatchNeedsYou, "status:x", "Blocked: no credentials"), events.AssistantNoticeNeedsAttention, `Session "Fix login" needs your attention`},
		{runNotice(protocol.WatchFinished, "reported|1", "Agent reported done"), events.AssistantNoticeFinished, `Session "Fix login" finished`},
		{runNotice(protocol.WatchFinished, "failed", "Failed: tests"), events.AssistantNoticeFinished, `Session "Fix login" failed`},
	} {
		if err := svc.Transcribe(t.Context(), "m1", tc.notice); err != nil {
			t.Fatalf("Transcribe %s: %v", tc.title, err)
		}
		e := <-sub.Events()
		p, ok := e.Payload.(events.AssistantNoticePayload)
		if !ok || e.WorkspaceID != "ws1" || e.RunID != "run-1" || e.ActorID != "m1" || p.MemberID != "m1" ||
			p.WatchID != "wat-1" || p.Kind != tc.kind || p.Title != tc.title || p.Body != tc.notice.Body {
			t.Fatalf("event = %+v, want %s %q", e, tc.kind, tc.title)
		}
	}
	b, err := os.ReadFile(svc.logPath("m1"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	for _, want := range []string{`needs your input`, `Question: Which region?`, `failed`, `[Stop or change this watch](?page=settings`, `watch=wat-1`} {
		if !strings.Contains(log, want) {
			t.Fatalf("transcript = %s, want %q", log, want)
		}
	}
}

func TestTranscribeWritesNothingForAMemberBeingRemoved(t *testing.T) {
	t.Parallel()
	svc, sub := newNoticeService(t)
	if err := svc.Fence(t.Context(), "fenced"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Transcribe(t.Context(), "fenced", runNotice(protocol.WatchFinished, "failed", "Failed")); !errors.Is(err, ErrRemoving) {
		t.Fatalf("Transcribe for a fenced member = %v, want ErrRemoving", err)
	}
	if _, err := os.Stat(filepath.Dir(svc.logPath("fenced"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("files were recreated for the fenced member: %v", err)
	}
	if len(sub.Events()) != 0 {
		t.Fatal("a notice nobody received was announced")
	}
}

func TestANoticeReachesTheTranscriptWhetherOrNotTheSessionTakesIt(t *testing.T) {
	t.Parallel()
	fix, err := acpmock.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := newNoticeService(t)
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	go acpmock.New(fix).Serve(agentIn, agentOut)
	sess, err := acphost.Start(t.Context(), hostIn, hostOut, acphost.Config{LogPath: svc.logPath("m1"), Cwd: t.TempDir(), Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	m := svc.member("m1")
	m.mu.Lock()
	m.sess = &session{acp: sess, ended: make(chan struct{})}
	m.mu.Unlock()

	if err := svc.Transcribe(t.Context(), "m1", runNotice(protocol.WatchFinished, "completed", "Completed")); err != nil {
		t.Fatalf("a live session: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	_ = agentOut.Close()
	_ = agentIn.Close()
	<-sess.Done()
	if err := svc.Transcribe(t.Context(), "m1", runNotice(protocol.WatchFinished, "failed", "Failed: after the close")); err != nil {
		t.Fatalf("a closed session: %v", err)
	}
	b, _ := os.ReadFile(svc.logPath("m1"))
	if log := string(b); !strings.Contains(log, "Completed") || !strings.Contains(log, "after the close") {
		t.Fatalf("transcript = %q, want both notices", log)
	}
}

func TestTranscribeReturnsWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	svc, _ := newNoticeService(t)
	m := svc.member("m1")
	m.op.Lock()
	defer m.op.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := svc.Transcribe(ctx, domain.MemberID("m1"), runNotice(protocol.WatchFinished, "failed", "Failed")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Transcribe with the op lock held and its context ended = %v, want context.Canceled", err)
	}
}
