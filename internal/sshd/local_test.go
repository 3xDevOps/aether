package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The in-process client is the server-hosted dashboard's transport. It
// must go through the same dispatch and subsystem handlers an SSH channel
// does, so every gate those enforce holds for a phone too.

func TestLocalCallDispatchesWithThePendingGate(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	l := e.srv.Local(e.member.ID)

	raw, perr := l.Call(ctx, protocol.MethodServerInfo, nil)
	if perr != nil {
		t.Fatalf("server.info: %v", perr)
	}
	var info protocol.ServerInfoResult
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if info.Member.ID != string(e.member.ID) {
		t.Fatalf("server.info member = %q, want %q", info.Member.ID, e.member.ID)
	}
	if _, perr = l.Call(ctx, "no.such", nil); perr == nil || perr.Code != protocol.CodeMethodNotFound {
		t.Fatalf("unknown method error = %v, want method not found", perr)
	}

	_, pm := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	lp := e.srv.Local(pm.ID)
	if _, perr = lp.Call(ctx, protocol.MethodServerInfo, nil); perr != nil {
		t.Fatalf("pending server.info: %v", perr)
	}
	_, perr = lp.Call(ctx, protocol.MethodRunList, nil)
	if perr == nil || perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "pending") {
		t.Fatalf("pending run.list error = %v, want the pending refusal", perr)
	}
}

func TestLocalAttachRunsTheAttachHandler(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	e.pty.replay = []byte("scrollback")
	l := e.srv.Local(e.member.ID)

	term, ack, err := l.Attach(ctx, protocol.AttachRequest{RunID: string(e.run.ID), Cols: 100, Rows: 40})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !ack.OK || ack.Cols != 100 || ack.Rows != 40 || ack.Replay != len("scrollback") {
		t.Fatalf("ack = %+v", ack)
	}
	replay := make([]byte, len("scrollback"))
	if _, rerr := io.ReadFull(term, replay); rerr != nil || string(replay) != "scrollback" {
		t.Fatalf("replay = %q (%v)", replay, rerr)
	}
	if _, werr := term.Write([]byte("hi")); werr != nil {
		t.Fatal(werr)
	}
	echo := make([]byte, len("echo:hi"))
	if _, rerr := io.ReadFull(term, echo); rerr != nil || string(echo) != "echo:hi" {
		t.Fatalf("echo = %q (%v)", echo, rerr)
	}
	if rerr := term.Resize(120, 50); rerr != nil {
		t.Fatal(rerr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cols, rows, readOnly, _, resizes := e.pty.state()
		if len(resizes) == 1 && resizes[0] == [2]uint{120, 50} {
			if cols != 100 || rows != 40 || readOnly {
				t.Fatalf("attach geometry = %dx%d readOnly=%v, want 100x40 writable", cols, rows, readOnly)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resize never reached the PTY host: %v", resizes)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cerr := term.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	_, ack, err = l.Attach(ctx, protocol.AttachRequest{RunID: "run_missing"})
	if err == nil || ack.OK || ack.Code != protocol.CodeNotFound {
		t.Fatalf("missing run: ack=%+v err=%v, want a not-found refusal", ack, err)
	}
}

func TestLocalAttachEndsWithTheRevocationExitStatus(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 20 * time.Millisecond })
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	_ = collab
	term, ack, err := e.srv.Local(cm.ID).Attach(context.Background(), protocol.AttachRequest{RunID: string(e.run.ID), Cols: 80, Rows: 24})
	if err != nil || !ack.OK {
		t.Fatalf("attach: ack=%+v err=%v", ack, err)
	}
	defer func() { _ = term.Close() }()
	if derr := e.store.DeleteMember(context.Background(), cm.ID); derr != nil {
		t.Fatal(derr)
	}
	_, err = io.ReadAll(term)
	var exit *protocol.RemoteExitError
	if !errors.As(err, &exit) || exit.Status != protocol.AttachExitMembershipRevoked {
		t.Fatalf("read after removal ended with %v, want exit status %d", err, protocol.AttachExitMembershipRevoked)
	}
}

func TestLocalEventsEndWhenMembershipIsRevoked(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 20 * time.Millisecond })
	_, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	stream, err := e.srv.Local(cm.ID).Events(context.Background(), protocol.SubscribeRequest{WorkspaceID: string(e.ws.ID)})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if derr := e.store.DeleteMember(context.Background(), cm.ID); derr != nil {
		t.Fatal(derr)
	}
	done := make(chan error, 1)
	go func() {
		_, rerr := io.ReadAll(stream)
		done <- rerr
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("events stream stayed open after the member was removed")
	}
}

func TestLocalEventsStreamsTheBus(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	stream, err := e.srv.Local(e.member.ID).Events(ctx, protocol.SubscribeRequest{WorkspaceID: string(e.ws.ID)})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if _, perr := e.bus.Publish(ctx, events.Event{
		WorkspaceID: e.ws.ID,
		RunID:       e.run.ID,
		ActorID:     e.member.ID,
		Payload:     events.PresencePayload{State: events.PresenceOnline},
	}); perr != nil {
		t.Fatal(perr)
	}
	line, err := protocol.ReadLine(bufio.NewReader(stream))
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	var ev protocol.Event
	if uerr := json.Unmarshal(line, &ev); uerr != nil {
		t.Fatal(uerr)
	}
	if ev.RunID != string(e.run.ID) || ev.Type != string(events.TypePresence) {
		t.Fatalf("event = %+v, want the published presence event", ev)
	}

	_, pm := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	_, err = e.srv.Local(pm.ID).Events(ctx, protocol.SubscribeRequest{})
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeDenied {
		t.Fatalf("pending subscribe error = %v, want a denied *protocol.Error", err)
	}
}

func TestLocalTerminalRunsTheTerminalHandler(t *testing.T) {
	e := newTestEnv(t, nil)
	term, ack, err := e.srv.Local(e.member.ID).Terminal(context.Background(), protocol.TerminalRequest{Tab: "main", Cols: 90, Rows: 30})
	if err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if !ack.OK || ack.Tab != "main" || ack.Cols != 90 || ack.Rows != 30 {
		t.Fatalf("ack = %+v", ack)
	}
	if _, err := term.Write([]byte("ls")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len("echo:ls"))
	if _, err := io.ReadFull(term, echo); err != nil || string(echo) != "echo:ls" {
		t.Fatalf("echo = %q (%v)", echo, err)
	}
	_ = term.Close()
}

func TestWebIdentityNamesWhyHTTPCannotBeIdentified(t *testing.T) {
	keyOnly := newTestEnv(t, nil)
	if err := keyOnly.srv.WebIdentity(); err == nil || !strings.Contains(err.Error(), "tailscaled") {
		t.Fatalf("key-only server: %v, want the missing-tailscaled reason", err)
	}
	requireKey := newTestEnv(t, func(c *Config) {
		c.WhoIs = &fakeWhoIs{}
		c.TailnetRequireKey = true
	})
	if err := requireKey.srv.WebIdentity(); err == nil || !strings.Contains(err.Error(), "tailnet-require-key") {
		t.Fatalf("require-key server: %v, want the require-key reason", err)
	}
	tailnet := newTestEnv(t, func(c *Config) { c.WhoIs = &fakeWhoIs{} })
	if err := tailnet.srv.WebIdentity(); err != nil {
		t.Fatalf("tailnet server: %v", err)
	}
}

func TestTailnetMemberMapsAddressesLikeSSHAuth(t *testing.T) {
	whois := &fakeWhoIs{id: WhoIsIdentity{Login: "alice@example.com", NodeID: "node-1"}}
	e := newFreshTestEnv(t, func(c *Config) { c.WhoIs = whois })
	ctx := context.Background()

	first, err := e.srv.TailnetMember(ctx, "100.64.0.1:40000")
	if err != nil {
		t.Fatal(err)
	}
	if first.Role != domain.RoleAdmin || first.Pending || first.TailnetLogin != "alice@example.com" {
		t.Fatalf("first contact = %+v, want an approved admin", first)
	}
	again, err := e.srv.TailnetMember(ctx, "100.64.0.1:40001")
	if err != nil || again.ID != first.ID {
		t.Fatalf("second lookup = %+v (%v), want the same member", again, err)
	}

	whois.set(WhoIsIdentity{Login: "bob@example.com", NodeID: "node-2"}, nil)
	second, err := e.srv.TailnetMember(ctx, "100.64.0.2:40000")
	if err != nil {
		t.Fatal(err)
	}
	if second.Role != domain.RoleCollaborator || !second.Pending {
		t.Fatalf("later contact = %+v, want a pending collaborator", second)
	}

	whois.set(WhoIsIdentity{NodeID: "node-ci", Tagged: true}, nil)
	if _, terr := e.srv.TailnetMember(ctx, "100.64.0.3:40000"); !errors.Is(terr, ErrTaggedNode) {
		t.Fatalf("tagged node error = %v, want ErrTaggedNode", terr)
	}

	whois.set(WhoIsIdentity{}, errors.New("tailscaled is down"))
	_, err = e.srv.TailnetMember(ctx, "100.64.0.4:40000")
	if err == nil || errors.Is(err, ErrTaggedNode) || !strings.Contains(err.Error(), "tailscaled is down") {
		t.Fatalf("resolver failure = %v, want the wrapped whois error", err)
	}
}

// A follow attach renders the session at the size it already is, so the
// ack has to report that size rather than echo the header, and a later
// resize by someone else has to reach the client while the attach is open.
// The in-process client is the server-hosted dashboard's transport; the
// SSH one is covered in transport_test.go.
func TestLocalAttachFollowsTheSessionGeometry(t *testing.T) {
	e := newTestEnv(t, nil)
	e.pty.session = [2]uint{132, 43}
	e.pty.tell = make(chan [2]uint, 1)

	term, ack, err := e.srv.Local(e.member.ID).Attach(context.Background(), protocol.AttachRequest{
		RunID:  string(e.run.ID),
		Cols:   80,
		Rows:   24,
		Follow: true,
		Framed: true,
	})
	if err != nil || !ack.OK {
		t.Fatalf("attach: ack=%+v err=%v", ack, err)
	}
	defer func() { _ = term.Close() }()
	if ack.Cols != 132 || ack.Rows != 43 {
		t.Fatalf("ack = %dx%d, want the session's 132x43 rather than the header's 80x24", ack.Cols, ack.Rows)
	}
	if !e.pty.following() {
		t.Fatal("the follow flag never reached the PTY host")
	}

	reader := &protocol.TerminalReader{Reader: term}
	sizeCh := make(chan [2]uint, 1)
	go func() {
		_, size, _ := reader.Read(make([]byte, 1))
		sizeCh <- size
	}()
	e.pty.tell <- [2]uint{120, 40}
	select {
	case size := <-sizeCh:
		if size != [2]uint{120, 40} {
			t.Fatalf("geometry = %v, want 120x40", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the session's resize never reached the attach")
	}
}
