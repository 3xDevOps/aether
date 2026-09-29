package edgeclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// connectTimeout bounds the relayed connection's opening: the edge answers
// once the server has attached, which it may take AttachDeadline to do.
const connectTimeout = edgeproto.AttachDeadline + 20*time.Second

// Dial opens a relayed connection to serverID through the edge, as the
// signed-in account on this device. The result carries the SSH stream end
// to end; the caller verifies the server's host key against serverID.
// ctx bounds only the opening; the connection lives until it is closed.
func (c *Client) Dial(ctx context.Context, serverID string) (net.Conn, error) {
	return c.dial(ctx, "connect to server "+serverID, serverID, edgeproto.ConnectPath(serverID))
}

// DialClaim opens a relayed claim connection to serverID, which the edge
// admits only while the server has no owner. The claim code travels in
// the SSH user name of that connection, inside SSH, so the caller must
// verify the host key against serverID before it authenticates.
func (c *Client) DialClaim(ctx context.Context, serverID string) (net.Conn, error) {
	return c.dial(ctx, "claim server "+serverID, serverID, edgeproto.ClaimConnectPath(serverID))
}

func (c *Client) dial(ctx context.Context, op, serverID, path string) (net.Conn, error) {
	if !edgeproto.ValidServerID(serverID) {
		return nil, fmt.Errorf("%s: %q is not a server id", op, serverID)
	}
	s, err := c.session()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	// websocket.NetConn reports placeholder addresses for a dialed
	// socket; the transport's own connection supplies real ones, which
	// the SSH layer parses as host:port.
	var (
		mu            sync.Mutex
		local, remote net.Addr
	)
	transport := newTransport(func(conn net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		local, remote = conn.LocalAddr(), conn.RemoteAddr()
	})
	defer transport.CloseIdleConnections()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+s.Token)
	header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	ws, resp, err := websocket.Dial(dialCtx, c.origin+path, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport, CheckRedirect: noRedirect},
		HTTPHeader: header,
	})
	if err != nil {
		if resp == nil || resp.StatusCode == http.StatusSwitchingProtocols {
			return nil, fmt.Errorf("%s: %s: %w", op, c.host, err)
		}
		if verr := checkVersion(c.host, resp); verr != nil {
			return nil, fmt.Errorf("%s: %w", op, verr)
		}
		// websocket.Dial keeps at most the first KiB of a refused body.
		body, _ := io.ReadAll(resp.Body)
		return nil, c.refusal(op, c.origin, resp.StatusCode, body)
	}
	if err := checkVersion(c.host, resp); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	mu.Lock()
	defer mu.Unlock()
	connCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &conn{
		Conn:   websocket.NetConn(connCtx, ws, websocket.MessageBinary),
		local:  local,
		remote: remote,
		stop:   stop,
	}, nil
}

// conn is a relayed stream with the addresses of the connection that
// carries it: to the edge, or to the proxy in between.
type conn struct {
	net.Conn
	local, remote net.Addr
	stop          context.CancelFunc
}

func (c *conn) LocalAddr() net.Addr  { return c.local }
func (c *conn) RemoteAddr() net.Addr { return c.remote }

func (c *conn) Close() error {
	err := c.Conn.Close()
	c.stop()
	return err
}
