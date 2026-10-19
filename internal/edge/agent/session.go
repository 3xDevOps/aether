package edgeagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/version"
	"github.com/coder/websocket"
)

// drained ends a session whose edge sent drain as it stopped.
type drained struct{ edgeproto.Drain }

func (drained) Error() string { return "edge is restarting (drain)" }

// wait is the drain's hint: RetryAfterMS plus a random part of JitterMS,
// each clamped to [0, limit] so a hint out of range neither fails the
// message nor parks the server.
func (d drained) wait(limit time.Duration) time.Duration {
	clamp := func(ms int64) time.Duration {
		return time.Duration(min(max(ms, 0), limit.Milliseconds())) * time.Millisecond
	}
	wait := clamp(d.RetryAfterMS)
	if jitter := clamp(d.JitterMS); jitter > 0 {
		wait += rand.N(jitter)
	}
	return wait
}

// session is one enrolled control connection, at protocol version
// protocol.
type session struct {
	c        *websocket.Conn
	edgeKey  ed25519.PublicKey
	protocol int
	// since is when the session enrolled.
	since time.Time
	// beats holds the latest answered ping recordBeats has not written.
	beats chan time.Time

	mu sync.Mutex
	// transfers holds, by report id, each transfer waiting for the edge's
	// answer.
	transfers map[string]chan edgeproto.OwnerTransferResult
}

func (s *session) send(m edgeproto.Message) error {
	data, err := edgeproto.EncodeControl(m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := s.c.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("edgeagent: send: %w", err)
	}
	return nil
}

// dial opens a WebSocket that lives until ctx is done or the returned
// cancel is called. Only the handshake is bounded by timeout: net/http ties
// an upgraded connection to its request's context, so a WithTimeout context
// would close the connection when the timer fired. The zero DialOptions
// client is http.DefaultClient, which honours HTTPS_PROXY.
func dial(ctx context.Context, url string, header http.Header, timeout time.Duration) (*websocket.Conn, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(timeout, cancel)
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if !timer.Stop() {
		if c != nil {
			_ = c.CloseNow()
		}
		cancel()
		return nil, nil, fmt.Errorf("dial %s: no handshake within %s", url, timeout)
	}
	if err != nil {
		cancel()
		if resp != nil && resp.Body != nil {
			var body edgeproto.ErrorBody
			if json.NewDecoder(resp.Body).Decode(&body) == nil && body.Error != "" {
				return nil, nil, fmt.Errorf("dial %s: %s: %s", url, resp.Status, body.Error)
			}
		}
		return nil, nil, fmt.Errorf("dial %s: %w", url, err)
	}
	return c, cancel, nil
}

func readMessage(ctx context.Context, c *websocket.Conn, timeout time.Duration) (edgeproto.Message, error) {
	for {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		_, data, err := c.Read(rctx)
		expired := rctx.Err() == context.DeadlineExceeded
		cancel()
		if expired {
			return nil, fmt.Errorf("edge silent for %s", timeout)
		}
		if err != nil {
			return nil, err
		}
		m, err := edgeproto.DecodeControl(data)
		if errors.Is(err, edgeproto.ErrUnknownMessage) {
			slog.Debug("edge: skipping unknown control message", "error", err)
			continue
		}
		return m, err
	}
}

// enroll dials the control endpoint and answers the challenge. It returns
// the enrolled connection, the edge's ready and the protocol version the
// hello named once the edge key matches the pinned one, pinning it on
// first enrollment.
func (a *Agent) enroll(ctx context.Context) (*websocket.Conn, context.CancelFunc, edgeproto.Ready, int, error) {
	c, cancel, err := dial(ctx, a.origin+edgeproto.PathServerControl, nil, handshakeTimeout)
	if err != nil {
		return nil, nil, edgeproto.Ready{}, 0, err
	}
	c.SetReadLimit(edgeproto.MaxControlMessageSize)
	ready, protocol, err := a.handshake(ctx, c)
	if err != nil {
		_ = c.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
		cancel()
		return nil, nil, edgeproto.Ready{}, 0, err
	}
	return c, cancel, ready, protocol, nil
}

func (a *Agent) handshake(ctx context.Context, c *websocket.Conn) (edgeproto.Ready, int, error) {
	m, err := readMessage(ctx, c, handshakeTimeout)
	if err != nil {
		return edgeproto.Ready{}, 0, fmt.Errorf("read challenge: %w", err)
	}
	ch, ok := m.(edgeproto.Challenge)
	if !ok {
		return edgeproto.Ready{}, 0, fmt.Errorf("edge sent %T, want a challenge", m)
	}
	if err = edgeproto.CheckVersion(ch.Version); err != nil {
		return edgeproto.Ready{}, 0, fmt.Errorf("edge speaks protocol %d: %w", ch.Version, err)
	}
	// The signature names the origin this server dialed, never ch.Origin:
	// a challenge relayed from another edge must not enroll there.
	sig, err := edgeproto.SignEnrollment(a.cfg.HostKey, a.origin, ch.Nonce)
	if err != nil {
		return edgeproto.Ready{}, 0, err
	}
	protocol := min(edgeproto.Version, ch.Version)
	hello, err := edgeproto.EncodeControl(edgeproto.Hello{
		Version:      protocol,
		HostKey:      a.cfg.HostKey.PublicKey().Marshal(),
		Signature:    sig,
		AgentVersion: version.Version,
		Name:         a.name,
		AccessPolicy: a.policy,
	})
	if err != nil {
		return edgeproto.Ready{}, 0, err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	err = c.Write(wctx, websocket.MessageText, hello)
	cancel()
	if err != nil {
		return edgeproto.Ready{}, 0, fmt.Errorf("send hello: %w", err)
	}
	m, err = readMessage(ctx, c, handshakeTimeout)
	if err != nil {
		return edgeproto.Ready{}, 0, fmt.Errorf("read ready: %w", err)
	}
	ready, ok := m.(edgeproto.Ready)
	if !ok {
		return edgeproto.Ready{}, 0, fmt.Errorf("edge sent %T, want ready", m)
	}
	if ready.ServerID != a.serverID {
		return edgeproto.Ready{}, 0, fmt.Errorf("edge enrolled server %s, this server is %s", ready.ServerID, a.serverID)
	}
	pinned, err := a.state.PinnedKey()
	if err != nil {
		return edgeproto.Ready{}, 0, err
	}
	switch {
	case pinned == nil:
		if err := a.state.refuseEarlierPins(); err != nil {
			return edgeproto.Ready{}, 0, err
		}
		if err := a.state.Pin(ready.EdgeKey); err != nil {
			return edgeproto.Ready{}, 0, err
		}
		slog.Info("edge: pinned edge key", "edge", a.origin, "fingerprint", edgeproto.EdgeKeyFingerprint(ready.EdgeKey))
	case !bytes.Equal(pinned, ready.EdgeKey):
		return edgeproto.Ready{}, 0, pinMismatch(pinned, ready.EdgeKey)
	}
	return ready, protocol, nil
}

// session runs one control connection until it ends and returns how long
// it stayed enrolled with an accepted edge key.
func (a *Agent) session(ctx context.Context) (time.Duration, error) {
	c, cancel, ready, protocol, err := a.enroll(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer c.CloseNow() //nolint:errcheck // the close error of a dead session is not actionable
	start := time.Now()
	s := &session{c: c, edgeKey: ready.EdgeKey, protocol: protocol, since: start, beats: make(chan time.Time, 1),
		transfers: map[string]chan edgeproto.OwnerTransferResult{}}
	if err = a.setEdgeState(ready.State); err != nil {
		return time.Since(start), err
	}
	if err = a.reconcileOwner(s, ready.State); err != nil {
		return time.Since(start), err
	}
	// Live only now: a transfer's report must follow the owner reported
	// here, or the edge would record this older one last. The status says
	// connected only once transfers and claims can use the session.
	a.setLive(s)
	defer a.setLive(nil)
	a.state.writeStatus(a.connected(s, time.Now()))
	slog.Info("edge: connected", "edge", a.origin, "server_id", a.serverID, "state", ready.State)
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go a.pushDirectory(sctx, s)
	go a.ping(sctx, s)
	recorded := make(chan struct{})
	go func() {
		a.recordBeats(sctx, s)
		close(recorded)
	}()
	err = a.serve(sctx, s)
	// The disconnected status the caller writes must land after the last
	// heartbeat.
	stop()
	<-recorded
	return time.Since(start), err
}

// reconcileOwner makes the edge's record of this server's owner, state,
// agree with this server's, which only a claim code from this host or a
// transfer sets. An edge without an owner makes the server forget its own.
// An edge recording a claimed server is told the server's owner again: a
// transfer that got no answer may or may not have been recorded there. An
// edge recording an owner the server does not have is told the server is
// ownerless, so a claim code can claim it: the server keeps its owner per
// edge key, and has none for a key `aether-server edge trust` pinned in
// place of another.
func (a *Agent) reconcileOwner(s *session, state string) error {
	if state == edgeproto.StateUnclaimed {
		return a.forgetOwner()
	}
	owner, err := a.state.Owner()
	if err != nil {
		return err
	}
	if owner != nil {
		if err := edgeproto.CheckProvider(owner.Provider); err != nil {
			// A build from the v0.5.2-alpha.3 tag could record a Google
			// account as the owner, which no edge signs in any more.
			slog.Warn("edge: forgetting this server's owner", "edge", a.origin, "error", err)
			return a.disown(s, *owner)
		}
		return s.send(edgeproto.OwnerTransferred{ID: edgeproto.NewConnID(), Owner: edgeproto.AccountPrincipal(*owner)})
	}
	slog.Warn("edge: the edge records an owner this server does not have for its key; reporting the server ownerless. Claim it with a code from `aether-server edge claim-code`",
		"edge", a.origin)
	return s.send(edgeproto.Ownerless{})
}

// forgetOwner drops an owner the edge does not know. The edge records
// ownership only from this server's reports, so its owner removed the
// server there while it was offline, the owner's account was deleted, or
// the edge lost its data. Claiming again takes a new claim code from this
// host, and the server itself still admits only its existing admins as
// the claimant.
func (a *Agent) forgetOwner() error {
	owner, err := a.state.Owner()
	if err != nil || owner == nil {
		return err
	}
	if err := a.state.ClearOwner(); err != nil {
		return err
	}
	slog.Warn("edge: the edge does not know this server's owner; claim it again with a code from `aether-server edge claim-code`",
		"edge", a.origin, "owner_provider", owner.Provider, "owner_subject", owner.Subject)
	return nil
}

func (a *Agent) ping(ctx context.Context, s *session) {
	t := time.NewTicker(a.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.send(edgeproto.Ping{}); err != nil {
				return
			}
		}
	}
}

// connected is the status of live session s whose edge last answered at
// heartbeat.
func (a *Agent) connected(s *session, heartbeat time.Time) Status {
	return Status{Edge: a.origin, Connected: true, Since: s.since, PID: os.Getpid(), Heartbeat: heartbeat}
}

// answered queues the heartbeat at now for recordBeats, replacing one it
// has not written yet. Only serve calls it, so the send cannot block.
func (s *session) answered(now time.Time) {
	select {
	case <-s.beats:
	default:
	}
	s.beats <- now
}

// recordBeats writes s's heartbeats to the status file off the read loop,
// which a slow disk must not stall. A run of failed writes logs once.
func (a *Agent) recordBeats(ctx context.Context, s *session) {
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-s.beats:
			err := a.state.write(statusFile, a.connected(s, now))
			if err != nil && !failing {
				slog.Warn("edge: write status heartbeat; later failures are not logged until a write succeeds", "error", err)
			}
			failing = err != nil
		}
	}
}

// EdgeVersion is the protocol version of the enrolled control connection,
// 0 while there is none. Below edgeproto.VersionHTTP the edge does not
// relay MCP connector requests.
func (a *Agent) EdgeVersion() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.live == nil {
		return 0
	}
	return a.live.protocol
}

func (a *Agent) setLive(s *session) {
	a.mu.Lock()
	a.live = s
	a.mu.Unlock()
}

func (a *Agent) setEdgeState(state string) error {
	a.edgeStateMu.Lock()
	defer a.edgeStateMu.Unlock()
	a.mu.Lock()
	a.edgeReports++
	a.edgeState = state
	a.mu.Unlock()
	return a.state.writeEdgeState(a.serverID, state)
}

// setClaimed records the claim reported when edgeReports was reports,
// unless a state was recorded since: that one is newer than the claim.
func (a *Agent) setClaimed(reports uint64) error {
	a.edgeStateMu.Lock()
	defer a.edgeStateMu.Unlock()
	a.mu.Lock()
	stale := a.edgeReports != reports
	if !stale {
		a.edgeState = edgeproto.StateClaimed
	}
	a.mu.Unlock()
	if stale {
		return nil
	}
	return a.state.writeEdgeState(a.serverID, edgeproto.StateClaimed)
}

// pushDirectory sends the directory whenever it may have changed, and
// re-reads it every directoryRefresh for changes another process made,
// such as `aether-server device approve` accepting an invitation. An
// unchanged directory is not sent again. When the owner's identity is no
// longer a member's, the server is ownerless: the agent forgets the owner
// and tells the edge.
func (a *Agent) pushDirectory(ctx context.Context, s *session) {
	var sent []edgeproto.DirectoryEntry
	for {
		// The owner is read before the directory: a claim or a transfer
		// records an owner only once its member is in the directory.
		owner, err := a.state.Owner()
		var entries []edgeproto.DirectoryEntry
		if err == nil {
			entries, err = a.cfg.SSH.EdgeDirectory(ctx)
		}
		if err == nil && (sent == nil || !slices.Equal(sent, entries)) {
			if err = s.send(edgeproto.Directory{Entries: entries}); err == nil {
				sent = append(make([]edgeproto.DirectoryEntry, 0, len(entries)), entries...)
			}
		}
		if err == nil && owner != nil && !isMember(entries, *owner) {
			err = a.disown(s, *owner)
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("edge: push directory", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.cfg.SSH.EdgeDirectoryChanged():
		case <-time.After(a.directoryRefresh):
		}
	}
}

func (a *Agent) serve(ctx context.Context, s *session) error {
	for {
		m, err := readMessage(ctx, s.c, a.idleTimeout)
		if err != nil {
			return fmt.Errorf("control connection: %w", err)
		}
		switch m := m.(type) {
		case edgeproto.Ping:
			if err := s.send(edgeproto.Pong{}); err != nil {
				return err
			}
		case edgeproto.Pong:
			s.answered(time.Now())
		case edgeproto.Open:
			a.open(s, m)
		case edgeproto.DeviceRevoked:
			a.revokeDevice(m.DeviceID)
		case edgeproto.OwnerTransferResult:
			s.transferAnswered(m)
		case edgeproto.AccountDeleted:
			// Unanswered, the edge sends the deletion again at the next
			// enrollment.
			if err := a.cfg.SSH.EdgeAccountDeleted(ctx, m.Provider, m.Subject); err != nil {
				return fmt.Errorf("apply the deletion of %s account %s at the edge: %w", m.Provider, m.Subject, err)
			}
			if err := s.send(edgeproto.AccountDeletionApplied(m)); err != nil {
				return err
			}
		case edgeproto.Unenroll:
			if err := errors.Join(a.setEdgeState(edgeproto.StateUnclaimed), a.state.ClearOwner()); err != nil {
				return err
			}
			return errors.New("the owner removed this server at the edge; it stays reachable directly and over the tailnet")
		case edgeproto.Drain:
			return drained{m}
		default:
			return fmt.Errorf("edge sent %T on an enrolled connection", m)
		}
	}
}

// Leave tells the edge the operator removed this server, then forgets the
// owner and the claim code. The pinned edge key is kept.
func (a *Agent) Leave(ctx context.Context) error {
	c, cancel, _, _, err := a.enroll(ctx)
	if err != nil {
		return fmt.Errorf("edgeagent: leave %s: %w", a.origin, err)
	}
	defer cancel()
	s := &session{c: c}
	if err := s.send(edgeproto.Unenroll{}); err != nil {
		_ = c.CloseNow()
		return fmt.Errorf("edgeagent: leave %s: %w", a.origin, err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	return errors.Join(a.setEdgeState(edgeproto.StateUnclaimed), a.state.ClearOwner(), a.state.RemoveClaimCode())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
