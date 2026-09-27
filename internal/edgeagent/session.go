package edgeagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/version"
	"github.com/coder/websocket"
)

var errDrain = errors.New("edge is restarting (drain)")

// session is one enrolled control connection.
type session struct {
	c       *websocket.Conn
	edgeKey ed25519.PublicKey
	done    chan struct{}
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
// the enrolled connection and the edge's ready once the edge key matches
// the pinned one, pinning it on first enrollment.
func (a *Agent) enroll(ctx context.Context) (*websocket.Conn, context.CancelFunc, edgeproto.Ready, error) {
	c, cancel, err := dial(ctx, a.origin+edgeproto.PathServerControl, nil, handshakeTimeout)
	if err != nil {
		return nil, nil, edgeproto.Ready{}, err
	}
	c.SetReadLimit(edgeproto.MaxControlMessageSize)
	ready, err := a.handshake(ctx, c)
	if err != nil {
		_ = c.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
		cancel()
		return nil, nil, edgeproto.Ready{}, err
	}
	return c, cancel, ready, nil
}

func (a *Agent) handshake(ctx context.Context, c *websocket.Conn) (edgeproto.Ready, error) {
	m, err := readMessage(ctx, c, handshakeTimeout)
	if err != nil {
		return edgeproto.Ready{}, fmt.Errorf("read challenge: %w", err)
	}
	ch, ok := m.(edgeproto.Challenge)
	if !ok {
		return edgeproto.Ready{}, fmt.Errorf("edge sent %T, want a challenge", m)
	}
	if err = edgeproto.CheckVersion(ch.Version); err != nil {
		return edgeproto.Ready{}, fmt.Errorf("edge speaks protocol %d: %w", ch.Version, err)
	}
	// The signature names the origin this server dialed, never ch.Origin:
	// a challenge relayed from another edge must not enroll there.
	sig, err := edgeproto.SignEnrollment(a.cfg.HostKey, a.origin, ch.Nonce)
	if err != nil {
		return edgeproto.Ready{}, err
	}
	hello, err := edgeproto.EncodeControl(edgeproto.Hello{
		Version:      min(edgeproto.Version, ch.Version),
		HostKey:      a.cfg.HostKey.PublicKey().Marshal(),
		Signature:    sig,
		AgentVersion: version.Version,
		Name:         a.name,
	})
	if err != nil {
		return edgeproto.Ready{}, err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	err = c.Write(wctx, websocket.MessageText, hello)
	cancel()
	if err != nil {
		return edgeproto.Ready{}, fmt.Errorf("send hello: %w", err)
	}
	m, err = readMessage(ctx, c, handshakeTimeout)
	if err != nil {
		return edgeproto.Ready{}, fmt.Errorf("read ready: %w", err)
	}
	ready, ok := m.(edgeproto.Ready)
	if !ok {
		return edgeproto.Ready{}, fmt.Errorf("edge sent %T, want ready", m)
	}
	if ready.ServerID != a.serverID {
		return edgeproto.Ready{}, fmt.Errorf("edge enrolled server %s, this server is %s", ready.ServerID, a.serverID)
	}
	pinned, err := a.state.PinnedKey()
	if err != nil {
		return edgeproto.Ready{}, err
	}
	switch {
	case pinned == nil:
		if err := a.state.Pin(ready.EdgeKey); err != nil {
			return edgeproto.Ready{}, err
		}
		slog.Info("edge: pinned edge key", "edge", a.origin, "fingerprint", edgeproto.EdgeKeyFingerprint(ready.EdgeKey))
	case !bytes.Equal(pinned, ready.EdgeKey):
		return edgeproto.Ready{}, pinMismatch(pinned, ready.EdgeKey)
	}
	return ready, nil
}

// session runs one control connection until it ends and returns how long
// it stayed enrolled with an accepted edge key.
func (a *Agent) session(ctx context.Context) (time.Duration, error) {
	c, cancel, ready, err := a.enroll(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer c.CloseNow() //nolint:errcheck // the close error of a dead session is not actionable
	start := time.Now()
	s := &session{c: c, edgeKey: ready.EdgeKey, done: make(chan struct{})}
	defer close(s.done)
	a.setSession(s)
	defer a.setSession(nil)
	a.state.writeStatus(Status{Edge: a.origin, Connected: true, Since: start})
	slog.Info("edge: connected", "edge", a.origin, "server_id", a.serverID, "state", ready.State)

	if ready.State == edgeproto.StateUnclaimed {
		if err = a.forgetOwner(); err != nil {
			return time.Since(start), err
		}
	}
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go a.pushDirectory(sctx, s)
	go a.ping(sctx, s)
	err = a.serve(sctx, s)
	return time.Since(start), err
}

// forgetOwner drops an owner the edge does not know. The edge records
// ownership only from a claim it forwarded, so its owner removed the
// server there while it was offline, or the edge lost its data. Claiming
// again takes a new claim code from this host, and the server itself
// still admits only its existing admins as the claimant.
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

func (a *Agent) pushDirectory(ctx context.Context, s *session) {
	for {
		entries, err := a.cfg.SSH.EdgeDirectory(ctx)
		if err == nil {
			err = s.send(edgeproto.Directory{Entries: entries})
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("edge: push directory", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.cfg.SSH.EdgeDirectoryChanged():
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
		case edgeproto.Open:
			a.open(s, m)
		case edgeproto.Claim:
			a.claim(ctx, s, m)
		case edgeproto.WebRedeemResult:
			a.deliverRedeem(m)
		case edgeproto.DeviceRevoked:
			a.revokeDevice(m.DeviceID)
		case edgeproto.Unenroll:
			if err := a.state.ClearOwner(); err != nil {
				return err
			}
			return errors.New("the owner removed this server at the edge; it stays reachable directly and over the tailnet")
		case edgeproto.Drain:
			return errDrain
		default:
			return fmt.Errorf("edge sent %T on an enrolled connection", m)
		}
	}
}

// Leave tells the edge the operator removed this server, then forgets the
// owner and the claim code. The pinned edge key is kept.
func (a *Agent) Leave(ctx context.Context) error {
	c, cancel, _, err := a.enroll(ctx)
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
	if err := a.state.ClearOwner(); err != nil {
		return err
	}
	return a.state.RemoveClaimCode()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
