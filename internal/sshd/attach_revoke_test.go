package sshd

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// revocableEnv is gatedEnv with a fast re-validation clock.
func revocableEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 50 * time.Millisecond })
	e.pty.gate = NewWriteGate(e.store)
	return e
}

// rawAttachConn is an attach opened on a raw session channel, so a test can
// read the exit status the server sends when it ends the attach (x/crypto's
// Session.Wait cannot report a subsystem's status).
type rawAttachConn struct {
	ch   ssh.Channel
	r    *bufio.Reader
	exit <-chan uint32
}

func rawAttach(t *testing.T, e *testEnv, signer ssh.Signer, run domain.RunID, withPTY bool, shell ...string) (rawAttachConn, protocol.AttachResponse) {
	t.Helper()
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ch, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	exitCh := make(chan uint32, 1)
	go func() {
		defer close(exitCh)
		for req := range reqs {
			if req.Type == "exit-status" {
				var p struct{ Status uint32 }
				if ssh.Unmarshal(req.Payload, &p) == nil {
					select {
					case exitCh <- p.Status:
					default:
					}
				}
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()
	if withPTY {
		ptyReq := struct {
			Term          string
			Cols, Rows    uint32
			Width, Height uint32
			Modes         string
		}{Term: "xterm", Cols: 80, Rows: 24}
		if ok, rerr := ch.SendRequest("pty-req", true, ssh.Marshal(&ptyReq)); rerr != nil || !ok {
			t.Fatalf("pty-req: ok=%v err=%v", ok, rerr)
		}
	}
	if ok, rerr := ch.SendRequest("subsystem", true, ssh.Marshal(&struct{ Name string }{protocol.SubsystemAttach})); rerr != nil || !ok {
		t.Fatalf("subsystem: ok=%v err=%v", ok, rerr)
	}
	r := bufio.NewReader(ch)
	header := `{"run_id":"` + string(run) + `"}`
	if len(shell) > 0 {
		header = `{"run_id":"` + string(run) + `","shell":"` + shell[0] + `"}`
	}
	if _, err := ch.Write([]byte(header + "\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	return rawAttachConn{ch: ch, r: r, exit: exitCh}, ack
}

// typeAndEcho proves the attach is live and writable: the fake PTY echoes
// keystrokes back prefixed with "echo:".
func (c rawAttachConn) typeAndEcho(t *testing.T, keys string) {
	t.Helper()
	if _, err := c.ch.Write([]byte(keys)); err != nil {
		t.Fatalf("write keystrokes: %v", err)
	}
	got := make([]byte, len("echo:")+len(keys))
	if _, err := io.ReadFull(c.r, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "echo:"+keys {
		t.Fatalf("echo = %q, want echo:%s", got, keys)
	}
}

// expectExit waits for the server to end the attach with exactly want, then
// for the channel to reach EOF, which is what stops further keystrokes.
func (c rawAttachConn) expectExit(t *testing.T, want int) {
	t.Helper()
	select {
	case st, ok := <-c.exit:
		if !ok {
			t.Fatal("channel closed without an exit-status")
		}
		if int(st) != want {
			t.Fatalf("exit-status = %d, want %d", st, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("attach still open after 5s, want exit-status %d", want)
	}
	eof := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, c.r)
		eof <- err
	}()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatalf("channel ended with %v, want EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel still readable 5s after the exit-status")
	}
}

// expectOpen proves the attach outlives d without the server ending it.
func (c rawAttachConn) expectOpen(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case st, ok := <-c.exit:
		t.Fatalf("attach ended (exit-status %d, sent %v), want it kept open", st, ok)
	case <-time.After(d):
	}
}

func TestRunShellAttachDropsOnSteerAndMembershipRevocation(t *testing.T) {
	t.Run("steer", func(t *testing.T) {
		e := revocableEnv(t)
		collab, _ := addMember(t, e, "Shell collaborator", domain.RoleCollaborator, false)
		c, ack := rawAttach(t, e, collab, e.run.ID, true, "shell")
		if !ack.OK {
			t.Fatalf("ack = %+v, want ok", ack)
		}
		calls := e.runs.Calls()
		if len(calls) == 0 || !strings.HasPrefix(calls[len(calls)-1], "run-shell:"+string(e.run.ID)+":shell:") {
			t.Fatalf("RunController calls = %v, want run shell ensure", calls)
		}
		c.expectOpen(t, 4*e.srv.cfg.revalidateInterval)
		e.run.Protected = true
		if err := e.store.UpdateRun(context.Background(), e.run); err != nil {
			t.Fatalf("protect run: %v", err)
		}
		c.expectExit(t, protocol.AttachExitSteerRevoked)
	})

	t.Run("membership", func(t *testing.T) {
		e := revocableEnv(t)
		collab, cm := addMember(t, e, "Shell viewer", domain.RoleCollaborator, false)
		c, ack := rawAttach(t, e, collab, e.run.ID, true, "shell")
		if !ack.OK {
			t.Fatalf("ack = %+v, want ok", ack)
		}
		if err := e.store.DeleteMember(context.Background(), cm.ID); err != nil {
			t.Fatalf("delete member: %v", err)
		}
		c.expectExit(t, protocol.AttachExitMembershipRevoked)
	})
}

// A collaborator typing into a teammate's terminal is demoted to viewer
// mid-attach. The re-validation ends the attach with the steer-revoked
// status; before the demotion the same re-validation left it alone.
func TestAttachDropsWriterOnDemotion(t *testing.T) {
	e := revocableEnv(t)
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	c, ack := rawAttach(t, e, collab, e.run.ID, true)
	if !ack.OK {
		t.Fatalf("ack = %+v, want ok", ack)
	}
	c.typeAndEcho(t, "hi")
	c.expectOpen(t, 4*e.srv.cfg.revalidateInterval)

	cm.Role = domain.RoleViewer
	if err := e.store.UpdateMember(context.Background(), cm); err != nil {
		t.Fatalf("demote: %v", err)
	}
	c.expectExit(t, protocol.AttachExitSteerRevoked)
}

// Every other way a live writer loses steer ends the attach too, and
// losing the membership ends it with its own status.
func TestAttachDropsWriterOnEveryRevocationPath(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		setup  func(t *testing.T, e *testEnv, cody *domain.Member)
		revoke func(t *testing.T, e *testEnv, cody *domain.Member)
		want   int
	}{
		{
			name: "run protected",
			revoke: func(t *testing.T, e *testEnv, _ *domain.Member) {
				e.run.Protected = true
				if err := e.store.UpdateRun(ctx, e.run); err != nil {
					t.Fatal(err)
				}
			},
			want: protocol.AttachExitSteerRevoked,
		},
		{
			name: "workspace admins-only",
			revoke: func(t *testing.T, e *testEnv, _ *domain.Member) {
				if err := e.store.SetWorkspaceSteerOthers(ctx, e.ws.ID, domain.SteerOthersAdminsOnly); err != nil {
					t.Fatal(err)
				}
			},
			want: protocol.AttachExitSteerRevoked,
		},
		{
			name: "protected run handed off",
			setup: func(t *testing.T, e *testEnv, cody *domain.Member) {
				e.run.MemberID, e.run.Protected = cody.ID, true
				if err := e.store.UpdateRun(ctx, e.run); err != nil {
					t.Fatal(err)
				}
			},
			revoke: func(t *testing.T, e *testEnv, _ *domain.Member) {
				e.run.MemberID = e.member.ID
				if err := e.store.UpdateRun(ctx, e.run); err != nil {
					t.Fatal(err)
				}
			},
			want: protocol.AttachExitSteerRevoked,
		},
		{
			name: "member removed",
			revoke: func(t *testing.T, e *testEnv, cody *domain.Member) {
				if err := e.store.DeleteMember(ctx, cody.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: protocol.AttachExitMembershipRevoked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := revocableEnv(t)
			collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)
			if tc.setup != nil {
				tc.setup(t, e, cm)
			}
			c, ack := rawAttach(t, e, collab, e.run.ID, true)
			if !ack.OK {
				t.Fatalf("ack = %+v, want ok", ack)
			}
			c.typeAndEcho(t, "ok")
			tc.revoke(t, e, cm)
			c.expectExit(t, tc.want)
		})
	}
}

// A read-only attach never held steer, so losing it changes nothing: the
// viewer keeps watching through a protection flip, an admins-only policy,
// and their own demotion. Only losing the membership ends it.
func TestAttachReadOnlySurvivesSteerLossUntilMembershipGoes(t *testing.T) {
	ctx := context.Background()
	e := revocableEnv(t)
	e.pty.replay = []byte("out")
	collab, cm := addMember(t, e, "Cody", domain.RoleCollaborator, false)

	c, ack := rawAttach(t, e, collab, e.run.ID, false)
	if !ack.OK {
		t.Fatalf("ack = %+v, want ok", ack)
	}
	e.run.Protected = true
	if err := e.store.UpdateRun(ctx, e.run); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetWorkspaceSteerOthers(ctx, e.ws.ID, domain.SteerOthersAdminsOnly); err != nil {
		t.Fatal(err)
	}
	cm.Role = domain.RoleViewer
	if err := e.store.UpdateMember(ctx, cm); err != nil {
		t.Fatal(err)
	}
	c.expectOpen(t, 6*e.srv.cfg.revalidateInterval)

	if err := e.store.DeleteMember(ctx, cm.ID); err != nil {
		t.Fatal(err)
	}
	c.expectExit(t, protocol.AttachExitMembershipRevoked)
}
