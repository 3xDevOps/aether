// Package edgeclient is the client side of an edge: device sign-in, the
// account's server list, claims, logout, and the relayed connection to a
// server. The device key and the device tokens live in the Aether config
// directory; a token is never printed, logged or put in an error.
package edgeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// DefaultURL is the edge the project runs.
const DefaultURL = "https://edge.onaether.dev"

const (
	requestTimeout  = 30 * time.Second
	tcpDialTimeout  = 10 * time.Second
	tlsTimeout      = 10 * time.Second
	maxResponseSize = 1 << 20
	// maxShownText bounds the refusal text an error quotes.
	maxShownText = 1024
)

// ErrNotSignedIn reports that this machine holds no device token for the
// edge.
var ErrNotSignedIn = errors.New("not signed in")

// Client talks to one edge on behalf of this machine.
type Client struct {
	origin string
	host   string
	dir    string
	http   *http.Client
}

// New returns a client for the edge at edgeURL. dir is the Aether config
// directory, which holds the device key and the device tokens.
func New(dir, edgeURL string) (*Client, error) {
	origin, err := edgeproto.Origin(edgeURL)
	if err != nil {
		return nil, fmt.Errorf("edge URL %q: %w", edgeURL, err)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return nil, fmt.Errorf("edge URL %q: %w", edgeURL, err)
	}
	return &Client{
		origin: origin,
		host:   u.Host,
		dir:    dir,
		http:   &http.Client{Transport: newTransport(nil), CheckRedirect: noRedirect},
	}, nil
}

// Choose returns the client for edgeURL. An empty edgeURL means the only
// edge dir holds a device token for, else DefaultURL.
func Choose(dir, edgeURL string) (*Client, error) {
	if edgeURL == "" {
		edgeURL = DefaultURL
		signedIn, err := SignedIn(dir)
		if err != nil {
			return nil, err
		}
		if len(signedIn) == 1 {
			edgeURL = signedIn[0]
		}
	}
	return New(dir, edgeURL)
}

// URL is the edge's canonical origin, "https://host[:port]".
func (c *Client) URL() string { return c.origin }

// Host is the edge's host name as errors and prompts name it.
func (c *Client) Host() string { return c.host }

// newTransport is the transport of every edge request. It honours
// HTTPS_PROXY and NO_PROXY. onDial, when set, sees each connection the
// transport opens.
func newTransport(onDial func(net.Conn)) *http.Transport {
	dialer := &net.Dialer{Timeout: tcpDialTimeout}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err == nil && onDial != nil {
				onDial(conn)
			}
			return conn, err
		},
		TLSHandshakeTimeout:   tlsTimeout,
		ResponseHeaderTimeout: requestTimeout,
	}
}

// noRedirect keeps a bearer token on the edge that was asked: an edge
// never redirects an API request, so a redirect is answered as an error.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// RefusedError is a request the edge refused, with the edge's own words.
type RefusedError struct {
	// Edge is the edge's host name.
	Edge string
	// URL is the edge's origin.
	URL string
	// Op says what was refused, such as "connect to server <id>".
	Op     string
	Status int
	// Message is the edge's refusal text, with control characters
	// replaced so it cannot drive the terminal it is printed to.
	Message string
}

func (e *RefusedError) Error() string {
	msg := fmt.Sprintf("%s: %s refused: %s (HTTP %d)", e.Op, e.Edge, e.Message, e.Status)
	if e.Status == http.StatusUnauthorized {
		msg += "; sign in again with: aether login --edge " + e.URL
	}
	return msg
}

// Is matches the edgeproto refusal the edge sent, so a caller can test
// errors.Is(err, edgeproto.RefusalNotConnected).
func (e *RefusedError) Is(target error) bool {
	r, ok := target.(edgeproto.Refusal)
	return ok && string(r) == e.Message
}

// refusal builds the error for a response that is not a success. body is
// what could be read of the response.
func (c *Client) refusal(op string, status int, body []byte) *RefusedError {
	var eb edgeproto.ErrorBody
	msg := http.StatusText(status)
	if json.Unmarshal(body, &eb) == nil && eb.Error != "" {
		msg = eb.Error
	} else if shown := strings.TrimSpace(string(body)); shown != "" {
		msg += ": " + shown
	}
	if len(msg) > maxShownText {
		msg = msg[:maxShownText] + "..."
	}
	return &RefusedError{Edge: c.host, URL: c.origin, Op: op, Status: status, Message: clean(msg)}
}

// clean replaces control characters and invalid UTF-8 in text the edge
// sent before it reaches a terminal.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "?")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// checkVersion refuses an edge whose announced protocol version is below
// the oldest this build accepts. An edge always announces one; a response
// without it came from something in between, such as a proxy, and is
// judged by its status instead.
func (c *Client) checkVersion(resp *http.Response) error {
	v := resp.Header.Get(edgeproto.HeaderVersion)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s sent protocol version %q", c.host, clean(v))
	}
	if err := edgeproto.CheckVersion(n); err != nil {
		return fmt.Errorf("%s speaks edge protocol %d: %w", c.host, n, err)
	}
	return nil
}

// call sends one JSON API request. token, when set, is the bearer token.
// A 2xx answer is decoded into out; anything else becomes a
// RefusedError.
func (c *Client) call(ctx context.Context, op, method, path, token string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s: encode request: %w", op, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, body)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	req.Header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("%s: read %s answer: %w", op, c.host, err)
	}
	if len(raw) > maxResponseSize {
		return fmt.Errorf("%s: %s answered more than %d bytes", op, c.host, maxResponseSize)
	}
	if err := c.checkVersion(resp); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.refusal(op, resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode %s answer: %w", op, c.host, err)
	}
	return nil
}

// Servers lists the servers the signed-in account reaches, as a member or
// as an invitee, and the domain the edge passes their dashboards through
// under, which is empty from an edge older than the field.
func (c *Client) Servers(ctx context.Context) (servers []edgeproto.ServerInfo, serverDomain string, err error) {
	s, err := c.session()
	if err != nil {
		return nil, "", err
	}
	var resp edgeproto.ServersResponse
	if err := c.call(ctx, "list servers", http.MethodGet, edgeproto.PathServers, s.Token, nil, &resp); err != nil {
		return nil, "", err
	}
	for _, info := range resp.Servers {
		if !edgeproto.ValidServerID(info.ID) || !printable(info.Name) || !printable(info.Role) {
			return nil, "", fmt.Errorf("list servers: %s sent a malformed server entry", c.host)
		}
	}
	if resp.ServerDomain != "" && !edgeproto.ValidServerDomain(resp.ServerDomain) {
		return nil, "", fmt.Errorf("list servers: %s sent a malformed server domain %q", c.host, resp.ServerDomain)
	}
	return resp.Servers, resp.ServerDomain, nil
}

// Claim presents a claim code, as printed by aether-server setup, for the
// signed-in account. The edge forwards it to the server, which compares
// it; on success the account owns the server.
func (c *Client) Claim(ctx context.Context, code string) (edgeproto.ClaimResponse, error) {
	normalized, id, err := edgeproto.ParseClaimCode(code)
	if err != nil {
		return edgeproto.ClaimResponse{}, err
	}
	s, err := c.session()
	if err != nil {
		return edgeproto.ClaimResponse{}, err
	}
	var resp edgeproto.ClaimResponse
	err = c.call(ctx, "claim server "+id, http.MethodPost, edgeproto.PathClaim, s.Token,
		edgeproto.ClaimRequest{Code: normalized}, &resp)
	if err != nil {
		return edgeproto.ClaimResponse{}, err
	}
	// Another id would pin this link to a server the code never named.
	if resp.ServerID != id || !printable(resp.Name) {
		return edgeproto.ClaimResponse{}, fmt.Errorf("claim server %s: %s answered with server %q, which the code does not name",
			id, c.host, clean(resp.ServerID))
	}
	return resp, nil
}

// printable reports whether text from the edge is safe to print: short,
// valid UTF-8, and free of control characters.
func printable(s string) bool {
	return len(s) <= 256 && clean(s) == s
}
