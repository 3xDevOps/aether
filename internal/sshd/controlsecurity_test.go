package sshd

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func expandedInfoPrefix() string {
	return `{"jsonrpc":"2.0","id":1,"method":"server.info","params":{"pad":"` + strings.Repeat("a", maxPendingLineBytes)
}

func waitControlFrames(t *testing.T, s *Server, members ...domain.MemberID) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		match := len(s.controlFrames) == len(members)
		for _, member := range members {
			_, held := s.controlFrames[member]
			match = match && held
		}
		s.mu.Unlock()
		if match {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("expanded frame operations did not settle to %v", members)
		}
	}
}

func writeControlBytes(t *testing.T, w io.Writer, data string) {
	t.Helper()
	if _, err := io.WriteString(w, data); err != nil {
		t.Fatalf("control write: %v", err)
	}
}

func readControlResponse(t *testing.T, r io.Reader) protocol.Response {
	t.Helper()
	type result struct {
		line []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := protocol.ReadLine(bufio.NewReader(r))
		done <- result{line, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("control response: %v", got.err)
		}
		var resp protocol.Response
		if err := json.Unmarshal(got.line, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	case <-time.After(5 * time.Second):
		t.Fatal("control response blocked")
		return protocol.Response{}
	}
}

func assertControlInfo(t *testing.T, pipe *subsystemPipe, member domain.MemberID) {
	t.Helper()
	writeControlBytes(t, pipe, `{"jsonrpc":"2.0","id":2,"method":"server.info"}`+"\n")
	resp := readControlResponse(t, pipe)
	var info protocol.ServerInfoResult
	if resp.Error != nil || json.Unmarshal(resp.Result, &info) != nil || info.Member.ID != string(member) {
		t.Fatalf("server.info did not answer for %s: %+v", member, resp)
	}
}

func TestControlExpandedFrameAdmission(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.dial(t)
	bobKey, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobClient, err := e.dialWith(bobKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bobClient.Close() })
	carolKey, carol := addMember(t, e, "Carol", domain.RoleCollaborator, false)
	carolClient, err := e.dialWith(carolKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = carolClient.Close() })

	first := openSubsystem(t, alice, protocol.SubsystemControl, nil)
	writeControlBytes(t, first, expandedInfoPrefix())
	waitControlFrames(t, e.srv, e.member.ID)

	// The per-member bound covers separate authenticated connections too.
	secondAlice := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	writeControlBytes(t, secondAlice, expandedInfoPrefix())
	if err := readLineWithin(t, bufio.NewReader(secondAlice)); err == nil {
		t.Fatal("one member acquired a second expanded frame")
	}

	second := openSubsystem(t, bobClient, protocol.SubsystemControl, nil)
	writeControlBytes(t, second, expandedInfoPrefix())
	waitControlFrames(t, e.srv, e.member.ID, bob.ID)
	third := openSubsystem(t, carolClient, protocol.SubsystemControl, nil)
	writeControlBytes(t, third, expandedInfoPrefix())
	if err := readLineWithin(t, bufio.NewReader(third)); err == nil {
		t.Fatal("a third member acquired an expanded frame")
	}

	// Full admission must not block ordinary calls, even on the connections
	// holding incomplete expanded frames or the one whose frame was refused.
	for _, caller := range []struct {
		client *ssh.Client
		member domain.MemberID
	}{{alice, e.member.ID}, {bobClient, bob.ID}, {carolClient, carol.ID}} {
		assertControlInfo(t, openSubsystem(t, caller.client, protocol.SubsystemControl, nil), caller.member)
	}
	for _, pipe := range []*subsystemPipe{first, second} {
		writeControlBytes(t, pipe, `"}}`+"\n")
		if resp := readControlResponse(t, pipe); resp.Error != nil {
			t.Fatalf("admitted frame failed: %+v", resp)
		}
	}
	waitControlFrames(t, e.srv)
	// The same persistent channel can acquire again after dispatch/response.
	writeControlBytes(t, first, expandedInfoPrefix()+`"}}`+"\n")
	if resp := readControlResponse(t, first); resp.Error != nil {
		t.Fatalf("subsequent expanded frame failed: %+v", resp)
	}
}

// A real SSH client whose packet reader can be paused. Unlike a normal
// x/crypto client it cannot acknowledge the server's channel-close packet.
// Close still releases the blocked I/O for deterministic test cleanup.
type pausedSSHConn struct {
	net.Conn
	paused atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func (c *pausedSSHConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if c.paused.Load() {
		<-c.closed
		return 0, net.ErrClosed
	}
	return n, err
}

func (c *pausedSSHConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestControlIncompleteFrameTimeoutClosesHostileTransport(t *testing.T) {
	// Serial: the short deadline is intentional, not a throughput test.
	e := newTestEnv(t, func(c *Config) {
		c.controlReadIdleTimeout = 300 * time.Millisecond
		c.controlFrameTimeout = 3 * time.Second
	})
	raw, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatal(err)
	}
	conn := &pausedSSHConn{Conn: raw, closed: make(chan struct{})}
	t.Cleanup(func() { _ = conn.Close() })
	sshConn, channels, requests, err := ssh.NewClientConn(conn, e.addr, &ssh.ClientConfig{
		User: "aether", Auth: []ssh.AuthMethod{ssh.PublicKeys(e.signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(sshConn, channels, requests)
	t.Cleanup(func() { _ = client.Close() })
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	conn.paused.Store(true)
	writeControlBytes(t, pipe, expandedInfoPrefix())
	waitControlFrames(t, e.srv, e.member.ID)
	waitControlFrames(t, e.srv)

	// The old reader has returned, not merely released its slot while keeping
	// its large buffer in a blocked goroutine. No peer close ACK was possible.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		e.srv.mu.Lock()
		remaining := len(e.srv.conns)
		e.srv.mu.Unlock()
		if remaining == 0 {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("expired frame left its SSH transport running")
		}
	}
	replacement := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	writeControlBytes(t, replacement, expandedInfoPrefix()+`"}}`+"\n")
	if resp := readControlResponse(t, replacement); resp.Error != nil {
		t.Fatalf("reconnect after frame timeout: %+v", resp)
	}
}

func TestControlFrameProgressRefreshesIdleDeadline(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.controlReadIdleTimeout = 500 * time.Millisecond
		c.controlFrameTimeout = 5 * time.Second
	})
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	writeControlBytes(t, pipe, expandedInfoPrefix())
	waitControlFrames(t, e.srv, e.member.ID)
	for range 8 {
		time.Sleep(100 * time.Millisecond)
		writeControlBytes(t, pipe, "a")
	}
	writeControlBytes(t, pipe, `"}}`+"\n")
	if resp := readControlResponse(t, pipe); resp.Error != nil {
		t.Fatalf("progressing frame failed: %+v", resp)
	}
}

func TestControlFrameAbsoluteDeadlineStopsTrickle(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.controlReadIdleTimeout = time.Second
		c.controlFrameTimeout = 600 * time.Millisecond
	})
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	writeControlBytes(t, pipe, expandedInfoPrefix())
	waitControlFrames(t, e.srv, e.member.ID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := pipe.Write([]byte("a")); err != nil {
				return
			}
		}
	}()
	if err := readLineWithin(t, bufio.NewReader(pipe)); err == nil {
		t.Fatal("unterminated trickle dispatched")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("absolute deadline did not terminate the trickling writer")
	}
	waitControlFrames(t, e.srv)
}

func TestControlExpandedAdmissionSpansDispatch(t *testing.T) {
	t.Parallel()
	br := &blockingRuns{fakeRuns: &fakeRuns{}, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	e := newTestEnv(t, func(c *Config) { c.Runs = br })
	t.Cleanup(func() { release.Do(func() { close(br.release) }) })
	pipe := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	params, err := json.Marshal(protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: strings.Repeat("t", maxPendingLineBytes), Harness: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := `{"jsonrpc":"2.0","id":1,"method":"run.launch","params":` + string(params) + "}"

	// Valid JSON without its NDJSON terminator must never dispatch on EOF.
	incomplete := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	writeControlBytes(t, incomplete, request)
	waitControlFrames(t, e.srv, e.member.ID)
	if err := incomplete.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := readLineWithin(t, bufio.NewReader(incomplete)); err == nil {
		t.Fatal("incomplete request was answered")
	}
	waitControlFrames(t, e.srv)
	select {
	case <-br.entered:
		t.Fatal("incomplete request reached launch")
	default:
	}

	writeControlBytes(t, pipe, request+"\n")
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("complete request never dispatched")
	}
	refused := openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil)
	writeControlBytes(t, refused, expandedInfoPrefix())
	if err := readLineWithin(t, bufio.NewReader(refused)); err == nil {
		t.Fatal("dispatch released expanded admission too early")
	}
	assertControlInfo(t, openSubsystem(t, e.dial(t), protocol.SubsystemControl, nil), e.member.ID)
	release.Do(func() { close(br.release) })
	if resp := readControlResponse(t, pipe); resp.Error != nil {
		t.Fatalf("launch failed: %+v", resp)
	}
	waitControlFrames(t, e.srv)
}

func TestControlIdleChannelsRemainUsable(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.controlReadIdleTimeout = 100 * time.Millisecond
		c.controlFrameTimeout = 200 * time.Millisecond
	})
	client := e.dial(t)
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	time.Sleep(350 * time.Millisecond)
	assertControlInfo(t, pipe, e.member.ID)
	time.Sleep(350 * time.Millisecond)
	assertControlInfo(t, pipe, e.member.ID)
	// Both the channel and its shared connection survive idle frame gaps.
	assertControlInfo(t, openSubsystem(t, client, protocol.SubsystemControl, nil), e.member.ID)
}

func TestControlExpandedAdmissionSpansBlockedResponse(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.controlReadIdleTimeout = 300 * time.Millisecond
		c.controlFrameTimeout = 5 * time.Second
	})
	client := e.dial(t)
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	// The fake run controller returns the task in its result. Exceed SSH's
	// 2 MiB receive window so the response cannot finish until we drain it.
	params, err := json.Marshal(protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: strings.Repeat("t", 3<<20), Harness: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeControlBytes(t, pipe, `{"jsonrpc":"2.0","id":1,"method":"run.launch","params":`+string(params)+"}\n")
	var first [1]byte
	if _, err := io.ReadFull(pipe, first[:]); err != nil {
		t.Fatalf("response never began: %v", err)
	}
	// No incoming progress is needed after a complete frame. Its absolute
	// deadline and admission still cover the blocked response.
	time.Sleep(450 * time.Millisecond)
	refused := openSubsystem(t, client, protocol.SubsystemControl, nil)
	writeControlBytes(t, refused, expandedInfoPrefix())
	if err := readLineWithin(t, bufio.NewReader(refused)); err == nil {
		t.Fatal("blocked response released expanded admission")
	}
	assertControlInfo(t, openSubsystem(t, client, protocol.SubsystemControl, nil), e.member.ID)
	resp := readControlResponse(t, io.MultiReader(strings.NewReader(string(first[:])), pipe))
	var result protocol.RunResult
	if resp.Error != nil || json.Unmarshal(resp.Result, &result) != nil || len(result.Run.Task) != 3<<20 {
		t.Fatalf("blocked response was not delivered intact: error=%v", resp.Error)
	}
	waitControlFrames(t, e.srv)
}

func TestControlCanceledRequestPreservesSiblingChannels(t *testing.T) {
	br := &blockingRuns{fakeRuns: &fakeRuns{}, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	e := newTestEnv(t, func(c *Config) { c.Runs = br })
	t.Cleanup(func() { release.Do(func() { close(br.release) }) })
	client := e.dial(t)
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	healthy := openSubsystem(t, client, protocol.SubsystemControl, nil)
	params, err := json.Marshal(protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: strings.Repeat("t", maxPendingLineBytes), Harness: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeControlBytes(t, pipe, `{"jsonrpc":"2.0","id":1,"method":"run.launch","params":`+string(params)+"}\n")
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never dispatched")
	}
	_ = pipe.Close()
	select {
	case <-br.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("channel closure did not cancel its request")
	}
	for range 3 {
		assertControlInfo(t, healthy, e.member.ID)
	}
	release.Do(func() { close(br.release) })
	waitControlFrames(t, e.srv)
}

func TestControlSmallRequestOutlivesFrameReadDeadline(t *testing.T) {
	br := &blockingRuns{fakeRuns: &fakeRuns{}, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	e := newTestEnv(t, func(c *Config) {
		c.Runs = br
		c.controlReadIdleTimeout = 100 * time.Millisecond
		c.controlFrameTimeout = 200 * time.Millisecond
	})
	t.Cleanup(func() { release.Do(func() { close(br.release) }) })
	client := e.dial(t)
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	healthy := openSubsystem(t, client, protocol.SubsystemControl, nil)
	params, err := json.Marshal(protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "ordinary launch", Harness: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeControlBytes(t, pipe, `{"jsonrpc":"2.0","id":1,"method":"run.launch","params":`+string(params)+"}\n")
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never dispatched")
	}
	time.Sleep(350 * time.Millisecond)
	assertControlInfo(t, healthy, e.member.ID)
	release.Do(func() { close(br.release) })
	if resp := readControlResponse(t, pipe); resp.Error != nil {
		t.Fatalf("ordinary launch failed: %+v", resp)
	}
}
