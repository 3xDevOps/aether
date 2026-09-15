package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
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

// resumeReleasePTY makes the fake's current output cursor observable for the
// release replacement without changing the shared fake used by other tests.
// A caught-up resume consumes no replay while still allowing the fake attach
// to exercise its live read/write lifecycle.
type resumeReleasePTY struct {
	*fakePTY
	cursor uint64
}

// admissionFailurePTY lets release tests fail the PTY-host commit after the
// control lease has been validated, without mutating the shared fake session.
type admissionFailurePTY struct {
	*fakePTY
	err error
}

func (p *admissionFailurePTY) Attach(ctx context.Context, key ptyhost.SessionKey, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error {
	if client.Commit == nil {
		return p.fakePTY.Attach(ctx, key, client, conn, resize)
	}
	return client.Commit(func() error { return p.err })
}

type resumeReplayConn struct {
	io.ReadWriter
	replay ptyhost.ReplayWriter
	resume ptyhost.ResumeWriter
	cursor uint64
}

func (c *resumeReplayConn) WriteReplay(io.Reader, int) error {
	c.resume.SetResume(c.cursor, true)
	return c.replay.WriteReplay(bytes.NewReader(nil), 0)
}

func (p *resumeReleasePTY) Attach(ctx context.Context, key ptyhost.SessionKey, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error {
	if client.Resume && client.Cursor == p.cursor {
		replay, ok := conn.(ptyhost.ReplayWriter)
		resume, resumed := conn.(ptyhost.ResumeWriter)
		if ok && resumed {
			conn = &resumeReplayConn{
				ReadWriter: conn,
				replay:     replay,
				resume:     resume,
				cursor:     p.cursor,
			}
			// Keep the replacement's input path observable. A read-only
			// client must not admit the bytes, while the connection must
			// remain able to carry unrelated output.
			inputSeen := make(chan struct{}, 1)
			client.InputAdmission = func(accept func() error) error {
				var err error
				if !client.ReadOnly {
					err = accept()
				}
				select {
				case inputSeen <- struct{}{}:
				default:
				}
				return err
			}
			go func() {
				<-inputSeen
				_, _ = conn.Write([]byte("independent output"))
			}()
		}
	}
	return p.fakePTY.Attach(ctx, key, client, conn, resize)
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
func rawAttachRequest(t *testing.T, e *testEnv, signer ssh.Signer, req protocol.AttachRequest, withPTY bool) (rawAttachConn, protocol.AttachResponse) {
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
		for request := range reqs {
			if request.Type == "exit-status" {
				var p struct{ Status uint32 }
				if ssh.Unmarshal(request.Payload, &p) == nil {
					select {
					case exitCh <- p.Status:
					default:
					}
				}
			}
			if request.WantReply {
				_ = request.Reply(false, nil)
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
	req.RunID = string(e.run.ID)
	header, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal attach header: %v", err)
	}
	if _, err := ch.Write(append(header, '\n')); err != nil {
		t.Fatalf("write header: %v", err)
	}
	r := bufio.NewReader(ch)
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	return rawAttachConn{ch: ch, r: r, exit: exitCh}, ack
}
func rawTerminal(t *testing.T, e *testEnv, signer ssh.Signer, withPTY bool, tab string) (rawAttachConn, protocol.TerminalResponse) {
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
	if ok, rerr := ch.SendRequest("subsystem", true, ssh.Marshal(&struct{ Name string }{protocol.SubsystemTerminal})); rerr != nil || !ok {
		t.Fatalf("subsystem: ok=%v err=%v", ok, rerr)
	}
	r := bufio.NewReader(ch)
	header, err := json.Marshal(protocol.TerminalRequest{Tab: tab})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	if _, err := ch.Write(append(header, '\n')); err != nil {
		t.Fatalf("write header: %v", err)
	}
	var ack protocol.TerminalResponse
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

func controlAttachEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, func(c *Config) { c.revalidateInterval = 10 * time.Millisecond })
	e.srv.cfg.Control = control.New(control.Config{})
	e.pty.gate = NewWriteGate(e.store)
	return e
}

func TestAttachControlLeasesAcrossSSHClients(t *testing.T) {
	t.Run("two tabs same member", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "tab-a"}, true)
		if !firstAck.OK || !firstAck.HasControl || firstAck.ControlGeneration == 0 {
			t.Fatalf("first ack = %+v, want held control", firstAck)
		}
		defer func() { _ = first.ch.Close() }()
		_, secondAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "tab-b"}, true)
		if secondAck.OK || secondAck.Code != protocol.CodeConflict {
			t.Fatalf("second tab ack = %+v, want occupied conflict", secondAck)
		}
	})

	t.Run("two members", func(t *testing.T) {
		e := controlAttachEnv(t)
		otherSigner, other := addMember(t, e, "Other", domain.RoleCollaborator, false)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "member-a"}, true)
		if !firstAck.OK {
			t.Fatalf("first ack = %+v", firstAck)
		}
		defer func() { _ = first.ch.Close() }()
		_, secondAck := rawAttachRequest(t, e, otherSigner, protocol.AttachRequest{ControlSessionID: "member-b"}, true)
		if secondAck.OK || secondAck.Code != protocol.CodeConflict {
			t.Fatalf("other member ack = %+v, want occupied conflict", secondAck)
		}
		_ = other
	})

	t.Run("explicit takeover displaces writer", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "old-tab"}, true)
		if !firstAck.OK {
			t.Fatalf("first ack = %+v", firstAck)
		}
		second, secondAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "new-tab", Takeover: true,
		}, true)
		if !secondAck.OK || !secondAck.HasControl || secondAck.ControlGeneration <= firstAck.ControlGeneration {
			t.Fatalf("takeover ack = %+v, first = %+v", secondAck, firstAck)
		}
		first.expectExit(t, protocol.AttachExitControlRevoked)
		_ = second.ch.Close()
	})

	t.Run("same session forced reconnect fences old transport", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "same-tab"}, true)
		if !firstAck.OK {
			t.Fatalf("first ack = %+v", firstAck)
		}
		second, secondAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "same-tab", ControlGeneration: firstAck.ControlGeneration, Takeover: true,
		}, true)
		if !secondAck.OK || !secondAck.HasControl || secondAck.ControlGeneration <= firstAck.ControlGeneration {
			t.Fatalf("forced reconnect ack = %+v, first = %+v", secondAck, firstAck)
		}
		first.expectExit(t, protocol.AttachExitControlRevoked)
		second.typeAndEcho(t, "still-writable")
		_ = second.ch.Close()
	})

	t.Run("delayed fence cancellation spares replacement generation", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "reused-tab"}, true)
		if !firstAck.OK {
			t.Fatalf("first ack = %+v", firstAck)
		}
		displaced, err := e.srv.cfg.Control.AdmitRevoke(string(e.run.ID), func() error { return nil })
		if err != nil || displaced == nil {
			t.Fatalf("atomic revoke = displaced %+v, error %v", displaced, err)
		}

		replacement, replacementAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "reused-tab"}, true)
		if !replacementAck.OK || !replacementAck.HasControl || replacementAck.ControlGeneration <= firstAck.ControlGeneration {
			t.Fatalf("replacement ack = %+v, first = %+v", replacementAck, firstAck)
		}
		e.srv.cancelControlAttach(string(e.run.ID), displaced.SessionID, displaced.Generation, errAttachControlRevoked)
		replacement.expectOpen(t, 50*time.Millisecond)
		replacement.typeAndEcho(t, "still-writable")
		_ = first.ch.Close()
		_ = replacement.ch.Close()
	})

	t.Run("late registration observes revoked generation", func(t *testing.T) {
		e := controlAttachEnv(t)
		acquired, _, err := e.srv.cfg.Control.Acquire(
			string(e.run.ID), string(e.member.ID), "late-tab", false,
		)
		if err != nil {
			t.Fatal(err)
		}
		if displaced, revokeErr := e.srv.cfg.Control.AdmitRevoke(string(e.run.ID), func() error { return nil }); revokeErr != nil || displaced == nil {
			t.Fatalf("atomic revoke = displaced %+v, error %v", displaced, revokeErr)
		}

		attachCtx, cancel := context.WithCancelCause(context.Background())
		id, registerErr := e.srv.registerControlAttach(string(e.run.ID), acquired.SessionID, acquired.Generation, cancel)
		defer e.srv.unregisterControlAttach(string(e.run.ID), acquired.SessionID, id)
		if !errors.Is(registerErr, control.ErrStale) {
			t.Fatalf("register error = %v, want %v", registerErr, control.ErrStale)
		}
		select {
		case <-attachCtx.Done():
			if !errors.Is(context.Cause(attachCtx), errAttachControlRevoked) {
				t.Fatalf("cancellation cause = %v, want %v", context.Cause(attachCtx), errAttachControlRevoked)
			}
		case <-time.After(time.Second):
			t.Fatal("late stale transport was not cancelled")
		}
	})

	t.Run("same tab reconnects within lease window", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "reconnect-tab"}, true)
		if !firstAck.OK {
			t.Fatalf("first ack = %+v", firstAck)
		}
		_ = first.ch.Close()
		time.Sleep(20 * time.Millisecond)
		second, secondAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "reconnect-tab", ControlGeneration: firstAck.ControlGeneration,
		}, true)
		if !secondAck.OK || !secondAck.HasControl || secondAck.ControlGeneration != firstAck.ControlGeneration {
			t.Fatalf("reconnect ack = %+v, first = %+v", secondAck, firstAck)
		}
		_ = second.ch.Close()
	})
	t.Run("release fences writer and resumes mirror", func(t *testing.T) {
		e := controlAttachEnv(t)
		e.pty.replay = []byte("history that must not be replayed")
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "release-tab"}, true)
		if !firstAck.OK {
			t.Fatalf("first ack = %+v", firstAck)
		}
		// Install a PTY seam that models the real host's resume decision. It
		// suppresses only the caught-up replay while preserving live attach
		// behavior, so this test proves the release request is a replacement
		// attach rather than a release-only response.
		e.srv.cfg.PTY = &resumeReleasePTY{fakePTY: e.pty, cursor: firstAck.Cursor}
		release, releaseAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID:  "release-tab",
			ControlGeneration: firstAck.ControlGeneration,
			ReleaseControl:    true,
			Resume:            true,
			Cursor:            firstAck.Cursor,
		}, true)
		defer func() { _ = release.ch.Close() }()
		if !releaseAck.OK || releaseAck.HasControl || !releaseAck.Resumed || releaseAck.Replay != 0 {
			t.Fatalf("release ack = %+v, want resumed read-only mirror with no replay", releaseAck)
		}
		if _, err := release.ch.Write([]byte("must-be-dropped")); err != nil {
			t.Fatalf("write release input: %v", err)
		}
		output := make([]byte, len("independent output"))
		if _, err := io.ReadFull(release.r, output); err != nil {
			t.Fatalf("read independent output: %v", err)
		}
		if string(output) != "independent output" {
			t.Fatalf("release output = %q, want independent output", output)
		}
		_, _, _, input, _ := e.pty.state()
		if input != "" {
			t.Fatalf("release mirror admitted input %q", input)
		}
		release.expectOpen(t, 50*time.Millisecond)
		first.expectExit(t, protocol.AttachExitControlRevoked)
	})

	t.Run("release missing session preserves old authority", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "release-missing-session",
		}, true)
		if !firstAck.OK || !firstAck.HasControl {
			t.Fatalf("first ack = %+v, want held control", firstAck)
		}
		defer func() { _ = first.ch.Close() }()

		e.pty.setErr(errNoSession)
		_, replacementAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID:  "release-missing-session",
			ControlGeneration: firstAck.ControlGeneration,
			ReleaseControl:    true,
		}, true)
		if replacementAck.OK || replacementAck.Code != protocol.CodeUnavailable {
			t.Fatalf("missing-session release ack = %+v, want unavailable refusal", replacementAck)
		}
		status, ok := e.srv.cfg.Control.Status(string(e.run.ID))
		if !ok || status.SessionID != "release-missing-session" ||
			status.Generation != firstAck.ControlGeneration || !status.Connected {
			t.Fatalf("authority after missing-session release = %+v/%v, want connected old generation %d", status, ok, firstAck.ControlGeneration)
		}
		first.typeAndEcho(t, "old-writer-still-admitted")
	})

	t.Run("release commit admission failure preserves old authority", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "release-commit-failure",
		}, true)
		if !firstAck.OK || !firstAck.HasControl {
			t.Fatalf("first ack = %+v, want held control", firstAck)
		}
		defer func() { _ = first.ch.Close() }()

		e.srv.cfg.PTY = &admissionFailurePTY{fakePTY: e.pty, err: ptyhost.ErrSessionEnded}
		_, replacementAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID:  "release-commit-failure",
			ControlGeneration: firstAck.ControlGeneration,
			ReleaseControl:    true,
		}, true)
		if replacementAck.OK || replacementAck.Code != protocol.CodeUnavailable {
			t.Fatalf("commit admission failure ack = %+v, want unavailable refusal", replacementAck)
		}
		status, ok := e.srv.cfg.Control.Status(string(e.run.ID))
		if !ok || status.SessionID != "release-commit-failure" ||
			status.Generation != firstAck.ControlGeneration || !status.Connected {
			t.Fatalf("authority after commit admission failure = %+v/%v, want connected old generation %d", status, ok, firstAck.ControlGeneration)
		}
		first.typeAndEcho(t, "commit-failed-old-writer-still-admitted")
	})

	t.Run("release finished-run admission failure does not replay", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "release-finished-session",
		}, true)
		if !firstAck.OK || !firstAck.HasControl {
			t.Fatalf("first ack = %+v, want held control", firstAck)
		}
		defer func() { _ = first.ch.Close() }()

		if err := e.store.UpdateRunStatus(context.Background(), e.run.ID, domain.RunCompleted, "", nil, nil); err != nil {
			t.Fatalf("complete run: %v", err)
		}
		e.pty.setTranscript(e.run.ID, []byte("finished transcript must not be replayed"))
		e.pty.setErr(errNoSession)
		_, replacementAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID:  "release-finished-session",
			ControlGeneration: firstAck.ControlGeneration,
			ReleaseControl:    true,
		}, true)
		if replacementAck.OK || replacementAck.Code != protocol.CodeUnavailable {
			t.Fatalf("finished-run release ack = %+v, want unavailable refusal", replacementAck)
		}
		status, ok := e.srv.cfg.Control.Status(string(e.run.ID))
		if !ok || status.SessionID != "release-finished-session" ||
			status.Generation != firstAck.ControlGeneration || !status.Connected {
			t.Fatalf("authority after finished-run release = %+v/%v, want connected old generation %d", status, ok, firstAck.ControlGeneration)
		}
		first.typeAndEcho(t, "finished-old-writer-still-admitted")
	})

	t.Run("release requires authenticated member and generation", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "member-release"}, true)
		if !firstAck.OK {
			t.Fatalf("first ack = %+v", firstAck)
		}
		defer func() { _ = first.ch.Close() }()
		otherSigner, _ := addMember(t, e, "Other release", domain.RoleCollaborator, false)
		_, crossMemberAck := rawAttachRequest(t, e, otherSigner, protocol.AttachRequest{
			ControlSessionID: "member-release", ControlGeneration: firstAck.ControlGeneration,
			ReleaseControl: true,
		}, false)
		if crossMemberAck.OK || crossMemberAck.Code != protocol.CodeConflict {
			t.Fatalf("cross-member release ack = %+v, want conflict", crossMemberAck)
		}
		_, omittedGenerationAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "member-release", ReleaseControl: true,
		}, false)
		if omittedGenerationAck.OK || omittedGenerationAck.Code != protocol.CodeInvalidParams {
			t.Fatalf("omitted-generation release ack = %+v, want invalid params", omittedGenerationAck)
		}
		status, ok := e.srv.cfg.Control.Status(string(e.run.ID))
		if !ok || status.MemberID != e.member.ID || status.Generation != firstAck.ControlGeneration {
			t.Fatalf("lease after refused releases = %+v/%v, want member %q generation %d", status, ok, e.member.ID, firstAck.ControlGeneration)
		}
	})

	t.Run("read-only watcher sees controller without acquiring", func(t *testing.T) {
		e := controlAttachEnv(t)
		first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "writer"}, true)
		if !firstAck.OK {
			t.Fatalf("writer ack = %+v", firstAck)
		}
		defer func() { _ = first.ch.Close() }()
		watcher, watcherAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
			ControlSessionID: "watcher", ReadOnly: true,
		}, true)
		if !watcherAck.OK || watcherAck.HasControl || watcherAck.ControllerID != string(e.member.ID) {
			t.Fatalf("watcher ack = %+v, want read-only controller status", watcherAck)
		}
		_ = watcher.ch.Close()
		first.typeAndEcho(t, "still-live")
	})
}

func TestOccupiedRunShellRemainsAvailableAsMirror(t *testing.T) {
	e := controlAttachEnv(t)
	first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "incumbent"}, true)
	if !firstAck.OK {
		t.Fatalf("incumbent ack = %+v", firstAck)
	}
	defer func() { _ = first.ch.Close() }()
	otherSigner, _ := addMember(t, e, "Shell member", domain.RoleCollaborator, false)
	_, ack := rawAttachRequest(t, e, otherSigner, protocol.AttachRequest{
		ControlSessionID: "shell-writer", Shell: "shared",
	}, true)
	if ack.OK || ack.Code != protocol.CodeConflict {
		t.Fatalf("occupied shell ack = %+v, want conflict", ack)
	}
	for _, call := range e.runs.Calls() {
		if call == "run-shell-stop:"+string(e.run.ID)+":shared" {
			t.Fatalf("occupied shell was destroyed instead of retained for a mirror: %v", e.runs.Calls())
		}
	}
	mirror, mirrorAck := rawAttachRequest(t, e, otherSigner, protocol.AttachRequest{
		ControlSessionID: "shell-writer", Shell: "shared", ReadOnly: true,
	}, true)
	if !mirrorAck.OK || mirrorAck.HasControl || mirrorAck.ControllerID != string(e.member.ID) {
		t.Fatalf("shell mirror ack = %+v, want current controller without control", mirrorAck)
	}
	_ = mirror.ch.Close()
}

func TestAttachTakeoverDoesNotEvictBeforeLateAdmission(t *testing.T) {
	e := controlAttachEnv(t)
	first, firstAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "incumbent"}, true)
	if !firstAck.OK || !firstAck.HasControl {
		t.Fatalf("incumbent ack = %+v, want held control", firstAck)
	}
	defer func() { _ = first.ch.Close() }()

	e.pty.mu.Lock()
	e.pty.gate = func(context.Context, domain.MemberID, ptyhost.SessionKey) error {
		return errors.New("late attach authorization failure")
	}
	e.pty.mu.Unlock()

	_, takeoverAck := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "takeover",
		Takeover:         true,
	}, true)
	if takeoverAck.OK {
		t.Fatalf("late-gated takeover ack = %+v, want refusal", takeoverAck)
	}
	snap, ok := e.srv.cfg.Control.Status(string(e.run.ID))
	if !ok || snap.SessionID != "incumbent" || snap.Generation != firstAck.ControlGeneration {
		t.Fatalf("controller after late rejection = %+v/%v, want incumbent generation %d", snap, ok, firstAck.ControlGeneration)
	}
	first.typeAndEcho(t, "incumbent-still-live")
}

func TestRunShellAttachDropsOnSteerAndMembershipRevocation(t *testing.T) {
	t.Parallel()
	t.Run("steer", func(t *testing.T) {
		t.Parallel()
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
		t.Parallel()
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

// A member removed while using their environment terminal loses the socket
// and receives the membership-revoked exit status.
func TestTerminalDropsOnMembershipRevocation(t *testing.T) {
	t.Parallel()
	e := revocableEnv(t)
	e.pty.replay = []byte("scrollback")
	// A fresh collaborator with no runs: deleting the run owner would trip
	// the runs foreign key, and revocation is about membership, not runs.
	collab, cm := addMember(t, e, "Terminal user", domain.RoleCollaborator, false)
	c, ack := rawTerminal(t, e, collab, true, "main")
	if !ack.OK || ack.Tab != "main" || ack.Cols != 80 || ack.Rows != 24 || ack.Replay != len(e.pty.replay) {
		t.Fatalf("terminal ack = %+v, want main 80x24 with replay %d", ack, len(e.pty.replay))
	}
	replay := make([]byte, len(e.pty.replay))
	if _, err := io.ReadFull(c.r, replay); err != nil {
		t.Fatalf("read terminal replay: %v", err)
	}
	if string(replay) != string(e.pty.replay) {
		t.Fatalf("terminal replay = %q, want %q", replay, e.pty.replay)
	}
	if calls := e.runs.Calls(); len(calls) < 2 ||
		calls[len(calls)-2] != "terminal:"+string(cm.ID) ||
		calls[len(calls)-1] != "terminal-tab:"+string(cm.ID)+":main:80:24" {
		t.Fatalf("RunController calls = %v, want terminal and main tab", calls)
	}
	if err := e.store.DeleteMember(context.Background(), cm.ID); err != nil {
		t.Fatalf("delete member: %v", err)
	}
	c.expectExit(t, protocol.AttachExitMembershipRevoked)
}

// A collaborator typing into a teammate's terminal is demoted to viewer
// mid-attach. The re-validation ends the attach with the steer-revoked
// status; before the demotion the same re-validation left it alone.
func TestAttachDropsWriterOnDemotion(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
			t.Parallel()
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
	t.Parallel()
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
