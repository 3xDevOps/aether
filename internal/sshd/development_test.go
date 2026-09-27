package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

// The transport suite supplies only process I/O; authorization and surface
// generations remain the real server/control implementation under test.
type developmentTestService struct{ env *testEnv }

func installDevelopmentTestService(e *testEnv) {
	if e.srv.cfg.Control == nil {
		e.srv.cfg.Control = control.New(control.Config{})
	}
	e.srv.cfg.Services.Development = &developmentTestService{env: e}
}

func (d *developmentTestService) Call(ctx context.Context, run domain.Run, principal control.Principal, method string, raw json.RawMessage, authorize func(context.Context) error) (any, error) {
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	if principal.Kind != control.PrincipalMember || principal.RunID != "" {
		panic("human transport spoofed principal")
	}
	if method != protocol.MethodDevTerminalList {
		return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unused test operation"}
	}
	return protocol.DevTerminalListResult{Terminals: []protocol.DevTerminal{
		{TerminalID: "shell", Incarnation: "shell-incarnation", Cols: 80, Rows: 24, Process: protocol.DevProcessState{State: "running"}},
		{TerminalID: "shared", Incarnation: "shared-incarnation", Cols: 80, Rows: 24, Process: protocol.DevProcessState{State: "running"}},
	}}, nil
}
func (d *developmentTestService) AttachTerminal(ctx context.Context, run domain.Run, principal control.Principal, target protocol.DevTerminalTarget, fence protocol.DevControlFence, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint, authorize func(context.Context) error) error {
	if err := authorize(ctx); err != nil {
		return err
	}
	conn.(ptyhost.TerminalResponderWriter).SetTerminalResponder(true)
	if !client.ReadOnly {
		client.InputAdmission = func(accept func() error) error {
			return d.env.srv.cfg.Control.AdmitSurface(string(run.ID), control.Surface{Kind: control.SurfaceTerminal, ID: target.TerminalID, Incarnation: target.Incarnation}, principal, fence.ControlSessionID, fence.ControlGeneration, func() error {
				if err := authorize(ctx); err != nil {
					return err
				}
				return accept()
			})
		}
	}
	return d.env.pty.Attach(ctx, ptyhost.RunShellSession(run.ID, target.TerminalID), client, conn, resize)
}
func (d *developmentTestService) BrowserFrames(ctx context.Context, run domain.Run, principal control.Principal, target protocol.DevBrowserPageTarget, authorize func(context.Context) error) (<-chan protocol.DevBrowserFrame, func(), error) {
	if err := authorize(ctx); err != nil {
		return nil, nil, err
	}
	return make(chan protocol.DevBrowserFrame), func() {}, nil
}
func (d *developmentTestService) OpenArtifact(ctx context.Context, run domain.Run, principal control.Principal, id string, authorize func(context.Context) error) (protocol.DevArtifact, io.ReadCloser, error) {
	if err := authorize(ctx); err != nil {
		return protocol.DevArtifact{}, nil, err
	}
	return protocol.DevArtifact{ID: id, RunID: string(run.ID), ContentType: "image/png", Bytes: 3}, io.NopCloser(strings.NewReader("png")), nil
}

func TestDevelopmentReadsRequireSteerAndAccountUse(t *testing.T) {
	e := newTestEnv(t, nil)
	installDevelopmentTestService(e)
	_, other := addMember(t, e, "observer", domain.RoleCollaborator, false)
	local := e.srv.Local(other.ID)
	raw, _ := json.Marshal(protocol.DevTerminalListParams{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}})
	if _, err := local.Call(t.Context(), protocol.MethodDevTerminalList, raw); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("read without account-use = %v", err)
	}
	if err := e.store.ShareAccount(t.Context(), e.member.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Call(t.Context(), protocol.MethodDevTerminalList, raw); err != nil {
		t.Fatal(err)
	}
	e.run.Protected = true
	if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Call(t.Context(), protocol.MethodDevTerminalList, raw); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("protected read = %v", err)
	}
}

func TestDevelopmentHumanCannotSupplyPrincipal(t *testing.T) {
	e := newTestEnv(t, nil)
	installDevelopmentTestService(e)
	raw := json.RawMessage(`{"run_id":"` + string(e.run.ID) + `","principal":{"kind":"run_agent"}}`)
	if _, err := e.srv.Local(e.member.ID).Call(t.Context(), protocol.MethodDevTerminalList, raw); err == nil || err.Code != protocol.CodeInvalidParams {
		t.Fatalf("spoofed principal = %v", err)
	}
}

func TestDevelopmentShellIncarnationAndSurfaceGeneration(t *testing.T) {
	e := controlAttachEnv(t)
	installDevelopmentTestService(e)
	first, ack := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", ControlSessionID: "first"}, true)
	if !ack.OK || !ack.HasControl || !ack.ServerOwnedResponder || ack.Incarnation != "shell-incarnation" {
		t.Fatalf("shell ack = %+v", ack)
	}
	defer func() { _ = first.ch.Close() }()
	_, wrong := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", Incarnation: "retired", ControlSessionID: "replacement"}, true)
	if wrong.OK || wrong.Code != protocol.CodeConflict {
		t.Fatalf("retired incarnation = %+v", wrong)
	}
	second, next := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", Incarnation: ack.Incarnation, ControlSessionID: "second", Takeover: true, ControlGeneration: ack.ControlGeneration}, true)
	if !next.OK || next.ControlGeneration <= ack.ControlGeneration {
		t.Fatalf("takeover = %+v", next)
	}
	defer func() { _ = second.ch.Close() }()
	first.expectExit(t, protocol.AttachExitControlRevoked)
	_, stale := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{Shell: "shell", Incarnation: ack.Incarnation, ControlSessionID: "first", ReleaseControl: true, ControlGeneration: ack.ControlGeneration}, true)
	if stale.OK || stale.Code != protocol.CodeConflict {
		t.Fatalf("stale release = %+v", stale)
	}
}

func TestDevelopmentBrowserIdleStreamClosesOnAccountRevocation(t *testing.T) {
	e := newTestEnv(t, nil)
	installDevelopmentTestService(e)
	_, other := addMember(t, e, "browser observer", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(t.Context(), e.member.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := e.srv.Local(other.ID).BrowserFrames(ctx, protocol.DevBrowserStreamRequest{DevBrowserPageTarget: protocol.DevBrowserPageTarget{DevBrowserTarget: protocol.DevBrowserTarget{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}, SessionID: "browser"}, PageID: "page", PageRevision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if revokeErr := e.store.RevokeAccountShare(t.Context(), e.member.ID, other.ID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	_, err = stream.Read(make([]byte, 1))
	var exit *protocol.RemoteExitError
	if !errors.As(err, &exit) || exit.Status != protocol.AttachExitSteerRevoked {
		t.Fatalf("idle account revocation = %v", err)
	}
}

type retainedArtifactTestService struct {
	handlerEvidenceService
	artifact protocol.DevArtifact
	source   io.ReadCloser
}

func (s *retainedArtifactTestService) OpenArtifact(context.Context, domain.WorkspaceID, string, string) (protocol.DevArtifact, io.ReadCloser, error) {
	return s.artifact, s.source, nil
}

func TestRetainedArtifactUsesEvidenceViewAfterRunShutdown(t *testing.T) {
	e := newTestEnv(t, nil)
	_, viewer := addMember(t, e, "evidence viewer", domain.RoleViewer, false)
	e.run.Status = domain.RunCompleted
	e.run.Protected = true
	if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
		t.Fatal(err)
	}
	packet := handlerPacket("retained-capture", e.ws.ID, e.run.ID, e.member.ID)
	packet.Captures = []protocol.DevArtifact{{ID: "capture", RunID: string(e.run.ID), ContentType: "image/png", Bytes: 3}}
	if err := e.store.CreateEvidencePacket(t.Context(), packet); err != nil {
		t.Fatal(err)
	}
	e.srv.cfg.Services.Development = nil
	e.srv.cfg.Services.Evidence = &retainedArtifactTestService{
		artifact: packet.Captures[0],
		source:   io.NopCloser(strings.NewReader("png")),
	}
	req := protocol.DevArtifactDownloadRequest{
		DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}, ArtifactID: "capture"},
		EvidencePacketID:     packet.ID,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, _, err := e.srv.Local(viewer.ID).Artifact(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "png" {
		t.Fatalf("retained read after shutdown = %q, %v", data, err)
	}
	req.EvidencePacketID = ""
	if transient, _, err := e.srv.Local(viewer.ID).Artifact(ctx, req); err == nil {
		_ = transient.Close()
		t.Fatal("evidence View authority admitted a transient capture")
	}
}

func TestRetainedArtifactRejectsWrongRunMissingCaptureAndExpiredPacket(t *testing.T) {
	e := newTestEnv(t, nil)
	packet := handlerPacket("scoped-capture", e.ws.ID, e.run.ID, e.member.ID)
	expired := handlerPacket("expired-capture", e.ws.ID, e.run.ID, e.member.ID)
	before := time.Now().Add(-time.Hour)
	expired.ExpiresAt = &before
	for _, item := range []*store.EvidencePacket{packet, expired} {
		if err := e.store.CreateEvidencePacket(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	e.srv.cfg.Services.Evidence = &retainedArtifactTestService{}
	for _, tc := range []struct {
		name, run, packet string
		code              int
	}{
		{"wrong run", "another-run", packet.ID, protocol.CodeNotFound},
		{"expired", string(e.run.ID), expired.ID, protocol.CodeInvalidState},
		{"capture not in packet", string(e.run.ID), packet.ID, protocol.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := protocol.DevArtifactDownloadRequest{
				DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: tc.run}, ArtifactID: "capture"},
				EvidencePacketID:     tc.packet,
			}
			_, _, _, err := e.srv.openDevelopmentArtifact(t.Context(), e.member.ID, req)
			var perr *protocol.Error
			if !errors.As(err, &perr) || perr.Code != tc.code {
				t.Fatalf("retained capture authority = %v, want %d", err, tc.code)
			}
		})
	}
}

func TestRetainedArtifactBlockedStreamStopsOnMembershipRevocation(t *testing.T) {
	e := newTestEnv(t, nil)
	_, viewer := addMember(t, e, "revoked evidence viewer", domain.RoleViewer, false)
	packet := handlerPacket("revocable-capture", e.ws.ID, e.run.ID, e.member.ID)
	packet.Captures = []protocol.DevArtifact{{ID: "capture", RunID: string(e.run.ID), ContentType: "image/png", Bytes: 3}}
	if err := e.store.CreateEvidencePacket(t.Context(), packet); err != nil {
		t.Fatal(err)
	}
	source, writer := io.Pipe()
	defer func() { _ = writer.Close(); _ = source.Close() }()
	e.srv.cfg.Services.Evidence = &retainedArtifactTestService{
		artifact: packet.Captures[0],
		source:   source,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, _, err := e.srv.Local(viewer.ID).Artifact(ctx, protocol.DevArtifactDownloadRequest{
		DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}, ArtifactID: "capture"},
		EvidencePacketID:     packet.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if removeErr := e.store.DeleteMember(t.Context(), viewer.ID); removeErr != nil {
		t.Fatal(removeErr)
	}
	_, err = stream.Read(make([]byte, 1))
	var exit *protocol.RemoteExitError
	if !errors.As(err, &exit) || exit.Status != protocol.AttachExitMembershipRevoked {
		t.Fatalf("retained stream after membership revocation = %v", err)
	}
}

// Stall the real SSH packet writer, not a source reader or a channel mock.
// Every encrypted packet after stall is enabled remains blocked until the
// server closes its raw connection, just as with a full TCP send buffer.
type stalledDevelopmentTransport struct {
	net.Conn
	stall        atomic.Bool
	blocked      chan struct{}
	closed       chan struct{}
	released     chan struct{}
	done         chan struct{}
	blockOnce    sync.Once
	closeOnce    sync.Once
	releasedOnce sync.Once
}

func (c *stalledDevelopmentTransport) Write(p []byte) (int, error) {
	if c.stall.Load() {
		c.blockOnce.Do(func() { close(c.blocked) })
		<-c.closed
		c.releasedOnce.Do(func() { close(c.released) })
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *stalledDevelopmentTransport) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}

func developmentSSHClient(t *testing.T, e *testEnv, signer ssh.Signer) (*ssh.Client, *stalledDevelopmentTransport) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan *stalledDevelopmentTransport, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		raw := &stalledDevelopmentTransport{
			Conn: conn, blocked: make(chan struct{}), closed: make(chan struct{}),
			released: make(chan struct{}), done: make(chan struct{}),
		}
		accepted <- raw
		defer close(raw.done)
		e.srv.handleConn(t.Context(), raw)
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User: "aether", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := <-accepted
	t.Cleanup(func() {
		_ = raw.Close()
		_ = client.Close()
		waitDevelopmentSignal(t, raw.done, 5*time.Second, "connection cleanup")
	})
	return client, raw
}

func waitDevelopmentSignal(t *testing.T, done <-chan struct{}, timeout time.Duration, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s did not finish within %s", what, timeout)
	}
}

func openDevelopmentSSHStream(t *testing.T, client *ssh.Client, subsystem string, header any) rawAttachConn {
	t.Helper()
	ch, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	exits := make(chan uint32, 1)
	go func() {
		defer close(exits)
		for req := range requests {
			if req.Type == "exit-status" {
				var status struct{ Status uint32 }
				if ssh.Unmarshal(req.Payload, &status) == nil {
					exits <- status.Status
				}
			}
			reply(req, false)
		}
	}()
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{subsystem}))
	if err != nil || !ok {
		t.Fatalf("open %s = %v, %v", subsystem, ok, err)
	}
	if writeErr := writeJSONLine(ch, header); writeErr != nil {
		t.Fatal(writeErr)
	}
	r := bufio.NewReader(ch)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatal(err)
	}
	var ack protocol.DevStreamResponse
	if err := json.Unmarshal(line, &ack); err != nil || !ack.OK {
		t.Fatalf("development ack = %+v, %v", ack, err)
	}
	return rawAttachConn{ch: ch, r: r, exit: exits}
}

type developmentObservedSource struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (s *developmentObservedSource) Close() error {
	err := s.ReadCloser.Close()
	s.once.Do(func() { close(s.closed) })
	return err
}

func TestRetainedArtifactStalledSSHTransportReleasesSource(t *testing.T) {
	for _, cause := range []string{"membership during data", "expiry during data", "write deadline", "membership during status"} {
		t.Run(cause, func(t *testing.T) {
			e := newTestEnv(t, nil)
			key, viewer := addMember(t, e, "stalled evidence viewer", domain.RoleViewer, false)
			packet := handlerPacket("stalled-capture", e.ws.ID, e.run.ID, e.member.ID)
			packet.Captures = []protocol.DevArtifact{{ID: "capture", RunID: string(e.run.ID), ContentType: "image/png", Bytes: 64 << 10}}
			if err := e.store.CreateEvidencePacket(t.Context(), packet); err != nil {
				t.Fatal(err)
			}
			reader, writer := io.Pipe()
			source := &developmentObservedSource{ReadCloser: reader, closed: make(chan struct{})}
			t.Cleanup(func() { _ = writer.Close(); _ = source.Close() })
			e.srv.cfg.Services.Evidence = &retainedArtifactTestService{artifact: packet.Captures[0], source: source}
			client, raw := developmentSSHClient(t, e, key)
			openDevelopmentSSHStream(t, client, protocol.SubsystemDevArtifact, protocol.DevArtifactDownloadRequest{
				DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}, ArtifactID: "capture"},
				EvidencePacketID:     packet.ID,
			})
			raw.stall.Store(true)
			if cause != "membership during status" {
				written := make(chan error, 1)
				go func() { _, err := writer.Write(make([]byte, 32<<10)); written <- err }()
				select {
				case err := <-written:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("source bytes were not consumed")
				}
				waitDevelopmentSignal(t, raw.blocked, 3*time.Second, "outgoing SSH data")
			}
			timeout := 3 * time.Second
			switch cause {
			case "membership during data", "membership during status":
				if err := e.store.DeleteMember(t.Context(), viewer.ID); err != nil {
					t.Fatal(err)
				}
			case "expiry during data":
				if err := e.store.(store.EvidenceExpiryStore).MarkEvidenceExpired(t.Context(), packet.ID, time.Now(), nil); err != nil {
					t.Fatal(err)
				}
			case "write deadline":
				timeout += developmentStreamWriteTimeout
			}
			if cause == "membership during status" {
				waitDevelopmentSignal(t, raw.blocked, 3*time.Second, "outgoing SSH status")
				// Cancellation must not wait for the close deadline to break
				// the blocked exit-status packet.
				timeout = sshChannelCloseTimeout / 2
			}
			waitDevelopmentSignal(t, source.closed, timeout, "source cancellation")
			select {
			case <-raw.closed:
				t.Fatal("source closure waited for raw transport abort")
			default:
			}
			waitDevelopmentSignal(t, raw.blocked, 3*time.Second, "outgoing SSH status")
			waitDevelopmentSignal(t, raw.closed, 3*time.Second, "raw transport abort")
			waitDevelopmentSignal(t, raw.released, 3*time.Second, "blocked packet writer")
			waitDevelopmentSignal(t, raw.done, 3*time.Second, "SSH connection handler")
		})
	}
}

func TestRetainedArtifactResponsiveSSHDownloadAndRevocation(t *testing.T) {
	for _, cause := range []string{"authorized download", "membership", "expiry"} {
		t.Run(cause, func(t *testing.T) {
			e := newTestEnv(t, nil)
			key, viewer := addMember(t, e, "responsive evidence viewer", domain.RoleViewer, false)
			packet := handlerPacket("responsive-capture", e.ws.ID, e.run.ID, e.member.ID)
			packet.Captures = []protocol.DevArtifact{{ID: "capture", RunID: string(e.run.ID), ContentType: "image/png", Bytes: 3}}
			if err := e.store.CreateEvidencePacket(t.Context(), packet); err != nil {
				t.Fatal(err)
			}
			source := io.NopCloser(strings.NewReader("png"))
			if cause != "authorized download" {
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				source = reader
			}
			e.srv.cfg.Services.Evidence = &retainedArtifactTestService{artifact: packet.Captures[0], source: source}
			client, raw := developmentSSHClient(t, e, key)
			other, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = other.Close() })
			stream := openDevelopmentSSHStream(t, client, protocol.SubsystemDevArtifact, protocol.DevArtifactDownloadRequest{
				DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}, ArtifactID: "capture"},
				EvidencePacketID:     packet.ID,
			})
			status := 0
			switch cause {
			case "authorized download":
				data, err := io.ReadAll(stream.r)
				if err != nil || string(data) != "png" {
					t.Fatalf("download = %q, %v", data, err)
				}
			case "membership":
				if err := e.store.DeleteMember(t.Context(), viewer.ID); err != nil {
					t.Fatal(err)
				}
				status = protocol.AttachExitMembershipRevoked
			case "expiry":
				if err := e.store.(store.EvidenceExpiryStore).MarkEvidenceExpired(t.Context(), packet.ID, time.Now(), nil); err != nil {
					t.Fatal(err)
				}
				status = 1
			}
			stream.expectExit(t, status)
			// This pre-existing channel must still exchange requests. Unknown
			// requests are denied normally, not by aborting the multiplex.
			if ok, err := other.SendRequest("keepalive@aether.test", true, nil); err != nil || ok {
				t.Fatalf("healthy sibling channel = %v, %v", ok, err)
			}
			select {
			case <-raw.closed:
				t.Fatal("responsive stream aborted its healthy SSH connection")
			default:
			}
		})
	}
}

type developmentBrowserStreamService struct {
	*developmentTestService
	frames chan protocol.DevBrowserFrame
	ctx    chan context.Context
	closed chan struct{}
}

func (s *developmentBrowserStreamService) BrowserFrames(ctx context.Context, _ domain.Run, _ control.Principal, _ protocol.DevBrowserPageTarget, authorize func(context.Context) error) (<-chan protocol.DevBrowserFrame, func(), error) {
	if err := authorize(ctx); err != nil {
		return nil, nil, err
	}
	s.ctx <- ctx
	return s.frames, func() { close(s.closed) }, nil
}

func TestDevelopmentBrowserStalledSSHTransportReleasesSubscription(t *testing.T) {
	e := newTestEnv(t, nil)
	key, member := addMember(t, e, "stalled browser viewer", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(t.Context(), e.member.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	service := &developmentBrowserStreamService{
		developmentTestService: &developmentTestService{env: e},
		frames:                 make(chan protocol.DevBrowserFrame, 1),
		ctx:                    make(chan context.Context, 1),
		closed:                 make(chan struct{}),
	}
	e.srv.cfg.Services.Development = service
	client, raw := developmentSSHClient(t, e, key)
	openDevelopmentSSHStream(t, client, protocol.SubsystemDevBrowser, protocol.DevBrowserStreamRequest{
		DevBrowserPageTarget: protocol.DevBrowserPageTarget{
			DevBrowserTarget: protocol.DevBrowserTarget{DevRunParams: protocol.DevRunParams{RunID: string(e.run.ID)}, SessionID: "browser"},
			PageID:           "page", PageRevision: 1,
		},
	})
	streamCtx := <-service.ctx
	raw.stall.Store(true)
	service.frames <- protocol.DevBrowserFrame{
		Metadata: protocol.DevBrowserFrameMetadata{RunID: string(e.run.ID), SessionID: "browser", PageID: "page"},
		Data:     []byte("pixels"),
	}
	waitDevelopmentSignal(t, raw.blocked, 3*time.Second, "outgoing browser frame")
	if err := e.store.RevokeAccountShare(t.Context(), e.member.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	waitDevelopmentSignal(t, streamCtx.Done(), 3*time.Second, "browser context cancellation")
	waitDevelopmentSignal(t, service.closed, 3*time.Second, "browser subscription closure")
	select {
	case <-raw.closed:
		t.Fatal("browser cancellation waited for raw transport abort")
	default:
	}
	waitDevelopmentSignal(t, raw.closed, 3*time.Second, "browser raw transport abort")
	waitDevelopmentSignal(t, raw.released, 3*time.Second, "browser packet writer")
	waitDevelopmentSignal(t, raw.done, 3*time.Second, "browser connection handler")
}
