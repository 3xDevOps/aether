package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// interactiveTestPTY supplies the live read-only transition that the real
// ptyhost session provides. fakePTY deliberately models the older static
// AttachClient fields, so these tests keep the authority transition in the
// server handler rather than pretending a release detached the stream.
type interactiveTestPTY struct {
	*fakePTY
	mu           sync.Mutex
	conn         io.Writer
	writable     bool
	beforeAttach func()
	beforeReady  func(bool)
}

func (p *interactiveTestPTY) Attach(ctx context.Context, key ptyhost.SessionKey, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error {
	p.mu.Lock()
	p.conn = conn
	p.writable = !client.ReadOnly
	before := p.beforeAttach
	p.mu.Unlock()

	admit := client.InputAdmission
	client.InputAdmission = func(accept func() error) error {
		if admit != nil {
			return admit(accept)
		}
		p.mu.Lock()
		writable := p.writable
		p.mu.Unlock()
		if !writable {
			return nil
		}
		return accept()
	}

	ready := false
	installReady := func() {
		if ready {
			return
		}
		ready = true
		if client.OnControlReady == nil {
			return
		}
		client.OnControlReady(func(readOnly bool) error {
			if p.beforeReady != nil {
				p.beforeReady(readOnly)
			}
			p.mu.Lock()
			p.writable = !readOnly
			p.mu.Unlock()
			return nil
		})
	}
	authorize := client.Authorize
	if authorize != nil {
		client.Authorize = func() error {
			err := authorize()
			if err == nil && before != nil {
				before()
				before = nil
			}
			return err
		}
	}
	commit := client.Commit
	if commit != nil {
		client.Commit = func(admit func() error) error {
			err := commit(admit)
			if err == nil {
				installReady()
			}
			return err
		}
	}
	attached := client.OnAttached
	client.OnAttached = func() {
		installReady()
		if attached != nil {
			attached()
		}
	}
	defer func() {
		p.mu.Lock()
		if p.conn == conn {
			p.conn = nil
		}
		p.mu.Unlock()
	}()
	return p.fakePTY.Attach(ctx, key, client, conn, resize)
}

func (p *interactiveTestPTY) emit(data []byte) error {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		return io.ErrClosedPipe
	}
	_, err := conn.Write(data)
	return err
}

type interactiveSSHAttach struct {
	pipe *subsystemPipe
	r    *bufio.Reader
	term protocol.TerminalReader
}

func openInteractiveSSHAttach(t *testing.T, e *testEnv, signer ssh.Signer, req protocol.AttachRequest) (*interactiveSSHAttach, protocol.AttachResponse) {
	t.Helper()
	pipe := openSubsystem(t, e.dialWithTest(t, signer), protocol.SubsystemAttach, func(sess *ssh.Session) error {
		ptyReq := struct {
			Term          string
			Cols, Rows    uint32
			Width, Height uint32
			Modes         string
		}{Term: "xterm", Cols: 80, Rows: 24}
		ok, err := sess.SendRequest("pty-req", true, ssh.Marshal(&ptyReq))
		if err != nil {
			return err
		}
		if !ok {
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	req.RunID = string(e.run.ID)
	req.Framed = true
	req.Interactive = true
	line, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal interactive attach: %v", err)
	}
	if _, err := pipe.Write(append(line, '\n')); err != nil {
		t.Fatalf("write interactive attach header: %v", err)
	}
	r := bufio.NewReader(pipe)
	var ack protocol.AttachResponse
	readJSONLine(t, r, &ack)
	return &interactiveSSHAttach{pipe: pipe, r: r, term: protocol.TerminalReader{Reader: r}}, ack
}

func (e *testEnv) dialWithTest(t *testing.T, signer ssh.Signer) *ssh.Client {
	t.Helper()
	client, err := e.dialWith(signer, nil)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func (c *interactiveSSHAttach) send(t *testing.T, ctl protocol.DashAttachControl) {
	t.Helper()
	line, err := json.Marshal(ctl)
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	if _, err := c.pipe.Write(append(line, '\n')); err != nil {
		t.Fatalf("write control: %v", err)
	}
}

func (c *interactiveSSHAttach) next(t *testing.T) ([]byte, *protocol.DashAttachControl) {
	t.Helper()
	type result struct {
		data []byte
		ctl  *protocol.DashAttachControl
		err  error
	}
	got := make(chan result, 1)
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, size, err := c.term.Read(buf)
			if n > 0 || size != [2]uint{} || c.term.Control != nil || err != nil {
				var ctl *protocol.DashAttachControl
				if c.term.Control != nil {
					copyCtl := *c.term.Control
					ctl = &copyCtl
				}
				got <- result{data: append([]byte(nil), buf[:n]...), ctl: ctl, err: err}
				return
			}
		}
	}()
	select {
	case res := <-got:
		if res.err != nil {
			t.Fatalf("read terminal record: %v", res.err)
		}
		return res.data, res.ctl
	case <-time.After(5 * time.Second):
		_ = c.pipe.Close()
		t.Fatal("timed out waiting for terminal record")
		return nil, nil
	}
}

func TestInteractiveAttachAcquireInputReleaseReacquire(t *testing.T) {
	e := controlAttachEnv(t)
	e.pty.replay = []byte("initial output")
	interactive := &interactiveTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = interactive

	wire, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "interactive-tab",
	})
	if !ack.OK || !ack.Framed || !ack.HasControl || ack.ControlGeneration == 0 {
		t.Fatalf("interactive ack = %+v, want writable control", ack)
	}
	if data, ctl := wire.next(t); string(data) != "initial output" || ctl != nil {
		t.Fatalf("initial record = %q/%+v, want output before controls", data, ctl)
	}

	generation := ack.ControlGeneration
	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachInput, Data: "first", ControlGeneration: generation,
	})
	if data, ctl := wire.next(t); string(data) != "echo:first" || ctl != nil {
		t.Fatalf("first input record = %q/%+v, want echo", data, ctl)
	}

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 1,
		ControlGeneration: generation,
	})
	_, release := wire.next(t)
	if release == nil || !release.OK || release.HasControl || release.ControlGeneration != generation {
		t.Fatalf("release response = %+v, want released generation %d", release, generation)
	}

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachInput, Data: "stale", ControlGeneration: generation,
	})
	_, stale := wire.next(t)
	if stale == nil || stale.OK || stale.Code != protocol.CodeConflict {
		t.Fatalf("stale input response = %+v, want conflict", stale)
	}
	_, _, _, input, _ := e.pty.state()
	if input != "first" {
		t.Fatalf("stale input reached PTY = %q, want only first", input)
	}

	if err := interactive.emit([]byte("watching while released")); err != nil {
		t.Fatalf("emit while released: %v", err)
	}
	if data, ctl := wire.next(t); string(data) != "watching while released" || ctl != nil {
		t.Fatalf("released output = %q/%+v, want same stream", data, ctl)
	}

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 2, Write: true,
	})
	_, reacquired := wire.next(t)
	if reacquired == nil || !reacquired.OK || !reacquired.HasControl || reacquired.ControlGeneration <= generation {
		t.Fatalf("reacquire response = %+v, want a new writable generation", reacquired)
	}
	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachInput, Data: "second", ControlGeneration: reacquired.ControlGeneration,
	})
	if data, ctl := wire.next(t); string(data) != "echo:second" || ctl != nil {
		t.Fatalf("reacquired input record = %q/%+v, want echo", data, ctl)
	}
}

func TestInteractiveAttachDuplicateAcquireRetainsAuthority(t *testing.T) {
	e := controlAttachEnv(t)
	interactive := &interactiveTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = interactive

	wire, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "duplicate-acquire-tab",
		ReadOnly:         true,
	})
	defer func() { _ = wire.pipe.Close() }()
	if !ack.OK || ack.HasControl {
		t.Fatalf("interactive ack = %+v, want a read-only mirror", ack)
	}

	// Queue two acquisitions before reading either result. The second sees the
	// lease granted by the first and must report that this attach still owns it.
	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 1, Write: true,
	})
	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 2, Write: true,
	})
	_, first := wire.next(t)
	_, second := wire.next(t)
	if first == nil || !first.OK || !first.HasControl || first.ControlGeneration == 0 {
		t.Fatalf("first acquisition = %+v, want writable control", first)
	}
	if second == nil || second.OK || second.Code != protocol.CodeConflict ||
		!second.HasControl || second.ControlGeneration != first.ControlGeneration {
		t.Fatalf("duplicate result = %+v, want conflict retaining generation %d",
			second, first.ControlGeneration)
	}

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachInput, Data: "still-owner",
		ControlGeneration: first.ControlGeneration,
	})
	if data, ctl := wire.next(t); string(data) != "echo:still-owner" || ctl != nil {
		t.Fatalf("post-refusal input record = %q/%+v, want echo", data, ctl)
	}
}

func TestInteractiveAttachLaterGrantRevocationContinuesWatching(t *testing.T) {
	e := controlAttachEnv(t)
	interactive := &interactiveTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = interactive

	wire, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "later-grant-tab",
	})
	defer func() { _ = wire.pipe.Close() }()
	if !ack.OK || !ack.HasControl || ack.ControlGeneration == 0 {
		t.Fatalf("interactive ack = %+v, want writable control", ack)
	}
	initialGeneration := ack.ControlGeneration

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 1,
		ControlGeneration: initialGeneration,
	})
	_, released := wire.next(t)
	if released == nil || !released.OK || released.HasControl {
		t.Fatalf("release response = %+v, want released control", released)
	}

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 2, Write: true,
	})
	_, granted := wire.next(t)
	if granted == nil || !granted.OK || !granted.HasControl ||
		granted.ControlGeneration <= initialGeneration {
		t.Fatalf("later grant response = %+v, want newer writable generation", granted)
	}
	laterGeneration := granted.ControlGeneration

	if displaced, err := e.srv.cfg.Control.AdmitRevoke(string(e.run.ID), func() error { return nil }); err != nil || displaced == nil {
		t.Fatalf("invalidate later grant = displaced %+v, error %v", displaced, err)
	}
	_, revoked := wire.next(t)
	if revoked == nil || revoked.OK || revoked.HasControl ||
		revoked.ControlGeneration != laterGeneration {
		t.Fatalf("later revocation = %+v, want exact generation %d", revoked, laterGeneration)
	}

	if err := interactive.emit([]byte("still observing")); err != nil {
		t.Fatalf("emit after later revocation: %v", err)
	}
	if data, ctl := wire.next(t); string(data) != "still observing" || ctl != nil {
		t.Fatalf("post-revocation output = %q/%+v, want observing continuity", data, ctl)
	}

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 3, Write: true,
	})
	_, reacquired := wire.next(t)
	if reacquired == nil || !reacquired.OK || !reacquired.HasControl ||
		reacquired.ControlGeneration <= laterGeneration {
		t.Fatalf("reacquire response = %+v, want newer writable generation", reacquired)
	}
	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachInput, Data: "after-reacquire",
		ControlGeneration: reacquired.ControlGeneration,
	})
	if data, ctl := wire.next(t); string(data) != "echo:after-reacquire" || ctl != nil {
		t.Fatalf("reacquired input record = %q/%+v, want echo", data, ctl)
	}
}

func TestInteractiveAttachRevocationPreservesObserving(t *testing.T) {
	e := controlAttachEnv(t)
	collabSigner, collab := addMember(t, e, "Interactive collaborator", domain.RoleCollaborator, false)
	interactive := &interactiveTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = interactive

	wire, ack := openInteractiveSSHAttach(t, e, collabSigner, protocol.AttachRequest{
		ControlSessionID: "revoked-tab",
	})
	if !ack.OK || !ack.HasControl {
		t.Fatalf("interactive ack = %+v, want writable control", ack)
	}

	admin := controlClient(t, e)
	var role protocol.MemberRoleResult
	if err := admin.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
		MemberID: string(collab.ID), Role: string(domain.RoleViewer),
	}, &role); err != nil {
		t.Fatalf("demote collaborator: %v", err)
	}
	_, revoked := wire.next(t)
	if revoked == nil || revoked.OK || revoked.HasControl || revoked.ControlGeneration != ack.ControlGeneration {
		t.Fatalf("revocation response = %+v, want a control fence", revoked)
	}
	if err := interactive.emit([]byte("still observing")); err != nil {
		t.Fatalf("emit after revocation: %v", err)
	}
	if data, ctl := wire.next(t); string(data) != "still observing" || ctl != nil {
		t.Fatalf("post-revocation output = %q/%+v, want observing continuity", data, ctl)
	}

	wire.send(t, protocol.DashAttachControl{
		Type: protocol.DashAttachInput, Data: "denied", ControlGeneration: ack.ControlGeneration,
	})
	_, denied := wire.next(t)
	if denied == nil || denied.OK || denied.HasControl || denied.ControlGeneration != ack.ControlGeneration ||
		(denied.Code != protocol.CodeDenied && denied.Code != protocol.CodeConflict) {
		t.Fatalf("denied input response = %+v, want permission or stale-generation rejection", denied)
	}
	_, _, _, input, _ := e.pty.state()
	if input != "" {
		t.Fatalf("denied input reached PTY = %q", input)
	}
	if err := interactive.emit([]byte("observing remains")); err != nil {
		t.Fatalf("emit after denied input: %v", err)
	}
	if data, ctl := wire.next(t); string(data) != "observing remains" || ctl != nil {
		t.Fatalf("output after denied input = %q/%+v, want observing continuity", data, ctl)
	}
}

func TestInteractiveAttachRevocationUsesOldGeneration(t *testing.T) {
	e := controlAttachEnv(t)
	interactive := &interactiveTestPTY{fakePTY: e.pty}
	ready := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	interactive.beforeReady = func(readOnly bool) {
		if !readOnly {
			return
		}
		once.Do(func() {
			close(ready)
			<-release
		})
	}
	e.srv.cfg.PTY = interactive

	wire, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "old-generation-tab",
	})
	if !ack.OK || !ack.HasControl || ack.ControlGeneration == 0 {
		t.Fatalf("interactive ack = %+v, want writable control", ack)
	}
	oldGeneration := ack.ControlGeneration
	current, ok := e.srv.cfg.Control.Status(string(e.run.ID))
	if !ok || current.Generation != oldGeneration {
		t.Fatalf("initial control = %+v/%v, want generation %d", current, ok, oldGeneration)
	}

	fenceDone := make(chan struct{})
	go func() {
		e.srv.cfg.Control.Fence(string(e.run.ID))
		e.srv.cancelControlAttach(string(e.run.ID), current.SessionID, oldGeneration, errAttachControlRevoked)
		close(fenceDone)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("timed out waiting for old lease fence")
	}

	replacement, _, err := e.srv.cfg.Control.Acquire(
		string(e.run.ID), string(e.member.ID), "replacement-tab", false,
	)
	if err != nil {
		close(release)
		t.Fatalf("acquire replacement control: %v", err)
	}
	if replacement.Generation <= oldGeneration {
		close(release)
		t.Fatalf("replacement control = %+v, want generation newer than %d", replacement, oldGeneration)
	}
	if current, ok := e.srv.cfg.Control.Status(string(e.run.ID)); !ok || current.Generation != replacement.Generation {
		close(release)
		t.Fatalf("replacement status = %+v/%v, want generation %d", current, ok, replacement.Generation)
	}
	close(release)
	select {
	case <-fenceDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old lease fence completion")
	}

	_, revoked := wire.next(t)
	if revoked == nil || revoked.OK || revoked.HasControl {
		t.Fatalf("revocation response = %+v, want a control fence", revoked)
	}
	if revoked.ControlGeneration != oldGeneration {
		t.Fatalf("revocation generation = %d, want revoked generation %d (replacement is %d)",
			revoked.ControlGeneration, oldGeneration, replacement.Generation)
	}
}

func TestInteractiveAttachAckPrecedesQueuedControl(t *testing.T) {
	e := controlAttachEnv(t)
	e.pty.replay = []byte("replay")
	interactive := &interactiveTestPTY{fakePTY: e.pty}
	interactive.beforeAttach = func() {
		snap, ok := e.srv.cfg.Control.Status(string(e.run.ID))
		if !ok {
			return
		}
		e.srv.cfg.Control.Fence(string(e.run.ID))
		e.srv.cancelControlAttach(string(e.run.ID), snap.SessionID, snap.Generation, errAttachControlRevoked)
	}
	e.srv.cfg.PTY = interactive

	wire, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "queued-control-tab",
	})
	if !ack.OK || !ack.HasControl {
		t.Fatalf("interactive ack = %+v, want initial control", ack)
	}
	if data, ctl := wire.next(t); string(data) != "replay" || ctl != nil {
		t.Fatalf("queued-control first record = %q/%+v, want replay output", data, ctl)
	}
	_, ctl := wire.next(t)
	if ctl == nil || ctl.OK || ctl.HasControl || ctl.ControlGeneration != ack.ControlGeneration {
		t.Fatalf("queued control record = %+v, want post-replay fence", ctl)
	}
}

type blockedControlConn struct {
	mu            sync.Mutex
	data          bytes.Buffer
	release       chan struct{}
	controlStart  chan struct{}
	controlDone   chan struct{}
	controlOnce   sync.Once
	controlActive bool
	controlParts  int
}

func newBlockedControlConn() *blockedControlConn {
	return &blockedControlConn{
		release:      make(chan struct{}),
		controlStart: make(chan struct{}),
		controlDone:  make(chan struct{}),
	}
}

func (c *blockedControlConn) Read([]byte) (int, error) { return 0, io.EOF }

func (c *blockedControlConn) Write(p []byte) (int, error) {
	if len(p) > 0 && p[0] == 'c' {
		c.mu.Lock()
		if !c.controlActive {
			c.controlActive = true
			c.controlOnce.Do(func() { close(c.controlStart) })
		}
		c.mu.Unlock()
		<-c.release
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.data.Write(p)
	if c.controlActive {
		c.controlParts++
		if c.controlParts == 4 {
			close(c.controlDone)
		}
	}
	return len(p), nil
}

func (c *blockedControlConn) Close() error { return nil }
func (c *blockedControlConn) exit(int)     {}

func TestInteractiveControlQueueDoesNotBlockFence(t *testing.T) {
	ch := newBlockedControlConn()
	conn := newAttachConn(ch, bufio.NewReader(bytes.NewReader(nil)),
		&protocol.AttachResponse{OK: true}, true, nil)
	conn.interactive = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.startControlWriter(ctx, func(err error) { cancel() })
	go conn.controlWriter()

	if err := conn.WriteReplay(bytes.NewReader(nil), 0); err != nil {
		t.Fatalf("write replay: %v", err)
	}
	first := protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 1,
	}
	if err := conn.sendControl(first); err != nil {
		t.Fatalf("queue first control: %v", err)
	}
	select {
	case <-ch.controlStart:
	case <-time.After(time.Second):
		t.Fatal("control writer did not reach blocked transport")
	}

	second := protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, RequestID: 2,
	}
	enqueued := make(chan error, 1)
	go func() { enqueued <- conn.sendControl(second) }()
	select {
	case err := <-enqueued:
		if err != nil {
			t.Fatalf("queue second control: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fence enqueue waited for blocked control output")
	}

	close(ch.release)
	select {
	case <-ch.controlDone:
	case <-time.After(time.Second):
		t.Fatal("control writer did not flush queued records")
	}
	ch.mu.Lock()
	wire := append([]byte(nil), ch.data.Bytes()...)
	ch.mu.Unlock()
	ackEnd := bytes.IndexByte(wire, '\n')
	if ackEnd < 0 {
		t.Fatal("missing attach ack")
	}
	reader := protocol.TerminalReader{Reader: bytes.NewReader(wire[ackEnd+1:])}
	buf := make([]byte, 1)
	if _, _, err := reader.Read(buf); err != nil {
		t.Fatalf("read first control: %v", err)
	}
	if reader.Control == nil || reader.Control.RequestID != 1 {
		t.Fatalf("first control = %+v, want request 1", reader.Control)
	}
	if _, _, err := reader.Read(buf); err != nil {
		t.Fatalf("read second control: %v", err)
	}
	if reader.Control == nil || reader.Control.RequestID != 2 {
		t.Fatalf("second control = %+v, want request 2", reader.Control)
	}
}

func TestInteractiveGeometryCarriesOrderedHighWater(t *testing.T) {
	ch := newBlockedControlConn()
	close(ch.release)
	ack := &protocol.AttachResponse{OK: true}
	conn := newAttachConn(ch, bufio.NewReader(bytes.NewReader(nil)), ack, true, nil)
	conn.interactive = true
	conn.SetTerminalPosition(ptyhost.TerminalPosition{Epoch: "epoch", Sequence: 7}, true)
	if err := conn.WriteReplay(bytes.NewReader(nil), 0); err != nil {
		t.Fatalf("write replay: %v", err)
	}
	if _, err := conn.Write([]byte("abc")); err != nil {
		t.Fatalf("write output: %v", err)
	}
	conn.SetGeometry(100, 40)

	wire := ch.data.Bytes()
	ackEnd := bytes.IndexByte(wire, '\n')
	if ackEnd < 0 {
		t.Fatal("missing attach ack")
	}
	var gotAck protocol.AttachResponse
	if err := json.Unmarshal(wire[:ackEnd], &gotAck); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if got := gotAck.HighWater(); got.Epoch != "epoch" || got.Sequence != 7 || !gotAck.Resumed {
		t.Fatalf("ack high-water = %+v resumed=%v", got, gotAck.Resumed)
	}

	reader := protocol.TerminalReader{Reader: bytes.NewReader(wire[ackEnd+1:])}
	buf := make([]byte, 8)
	n, _, err := reader.Read(buf)
	if err != nil || string(buf[:n]) != "abc" {
		t.Fatalf("read output = %q, %v", buf[:n], err)
	}
	if _, _, err := reader.Read(buf); err != nil {
		t.Fatalf("read geometry control: %v", err)
	}
	if reader.Control == nil || reader.Control.Type != protocol.DashAttachGeometry {
		t.Fatalf("geometry control = %+v", reader.Control)
	}
	if got := reader.Control.HighWater(); got.Epoch != "epoch" || got.Sequence != 10 {
		t.Fatalf("geometry high-water = %+v, want epoch/10", got)
	}
}
