package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const testDomain = "servers.example.test"

type fakeDir struct {
	mu         sync.Mutex
	tokens     map[string]tokenEntry
	owners     map[string]bool
	members    map[string][]edgeproto.Account
	dirs       map[string][]edgeproto.DirectoryEntry
	webCodes   map[string]string
	unenrolled []string
}

type tokenEntry struct {
	account edgeproto.Account
	device  edgeproto.Device
}

func (d *fakeDir) Authenticate(_ context.Context, token string) (edgeproto.Account, edgeproto.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.tokens[token]
	if !ok {
		return edgeproto.Account{}, edgeproto.Device{}, edgeproto.RefusalTokenRevoked
	}
	return e.account, e.device, nil
}

// Admit reads members only, so a test can admit an account to a server
// the relay itself must still refuse.
func (d *fakeDir) Admit(_ context.Context, serverID string, a edgeproto.Account) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	members, ok := d.members[serverID]
	if !ok {
		return edgeproto.RefusalUnknownServer
	}
	for _, m := range members {
		if sameAccount(m, a) {
			return nil
		}
	}
	return edgeproto.RefusalNotMember
}

func (d *fakeDir) Claimed(_ context.Context, serverID, _ string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.owners[serverID], nil
}

func (d *fakeDir) ReplaceDirectory(_ context.Context, serverID string, entries []edgeproto.DirectoryEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dirs[serverID] = entries
	return nil
}

func (d *fakeDir) RedeemWebCode(_ context.Context, serverID string, m edgeproto.WebRedeem) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	grant, ok := d.webCodes[serverID+" "+m.Code]
	if !ok {
		return "", edgeproto.Refusal("web sign-in code is not valid")
	}
	return grant, nil
}

func (d *fakeDir) Unenroll(_ context.Context, serverID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.unenrolled = append(d.unenrolled, serverID)
	return nil
}

func (d *fakeDir) addMember(serverID string, a edgeproto.Account) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.members[serverID] = append(d.members[serverID], a)
}

type fakeEgress struct {
	mu     sync.Mutex
	months map[string]int64
}

func (f *fakeEgress) Egress(_ context.Context, month string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.months[month], nil
}

func (f *fakeEgress) AddEgress(_ context.Context, month string, n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.months[month] += n
	return nil
}

type env struct {
	r       *Relay
	dir     *fakeDir
	egress  *fakeEgress
	edgeKey ed25519.PrivateKey
	origin  string
	base    string // http://127.0.0.1:port
	wsBase  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, 0, &fakeEgress{months: map[string]int64{}})
}

func newEnvWith(t *testing.T, budget int64, egress *fakeEgress) *env {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ts := httptest.NewUnstartedServer(mux)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	e := &env{
		dir: &fakeDir{
			tokens:   map[string]tokenEntry{},
			owners:   map[string]bool{},
			members:  map[string][]edgeproto.Account{},
			dirs:     map[string][]edgeproto.DirectoryEntry{},
			webCodes: map[string]string{},
		},
		egress:  egress,
		edgeKey: key,
	}
	e.origin = "http://localhost:" + strconv.Itoa(port)
	e.r, err = New(t.Context(), Config{
		Origin:       e.origin,
		ServerDomain: testDomain,
		EdgeKey:      key,
		Directory:    e.dir,
		Egress:       e.egress,
		EgressBudget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.r.Register(mux)
	ts.Start()
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		if err := e.r.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	e.base = ts.URL
	e.wsBase = "ws" + strings.TrimPrefix(ts.URL, "http")
	return e
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// addDevice registers a client install of account and returns its token.
func (e *env) addDevice(t *testing.T, account edgeproto.Account, deviceID string) (edgeproto.Device, string) {
	t.Helper()
	dev := edgeproto.Device{ID: deviceID, Label: "laptop", Key: edgeproto.DeviceKeyLine(newSigner(t).PublicKey())}
	token := edgeproto.NewToken()
	e.dir.mu.Lock()
	e.dir.tokens[token] = tokenEntry{account: account, device: dev}
	e.dir.mu.Unlock()
	return dev, token
}

func account(subject string) edgeproto.Account {
	return edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: subject, Login: "user" + subject}
}

// agent is a fake Aether server holding a control channel.
type agent struct {
	t      *testing.T
	e      *env
	signer ssh.Signer
	id     string
	ws     *websocket.Conn
	ready  edgeproto.Ready
	msgs   chan edgeproto.Message
	data   chan net.Conn
	// attach makes run answer every open by attaching a data socket.
	attach bool
}

func dialControl(t *testing.T, e *env) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(t.Context(), e.wsBase+edgeproto.PathServerControl, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(edgeproto.MaxControlMessageSize)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

func send(t *testing.T, ws *websocket.Conn, m edgeproto.Message) {
	t.Helper()
	if err := writeControl(t.Context(), ws, m); err != nil {
		t.Fatal(err)
	}
}

func recv(ctx context.Context, ws *websocket.Conn) (edgeproto.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return readControl(ctx, ws)
}

func challenge(t *testing.T, ws *websocket.Conn) edgeproto.Challenge {
	t.Helper()
	m, err := recv(t.Context(), ws)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := m.(edgeproto.Challenge)
	if !ok {
		t.Fatalf("got %T, want challenge", m)
	}
	return c
}

func helloFor(t *testing.T, signer ssh.Signer, origin string, nonce []byte) edgeproto.Hello {
	t.Helper()
	sig, err := edgeproto.SignEnrollment(signer, origin, nonce)
	if err != nil {
		t.Fatal(err)
	}
	return edgeproto.Hello{Version: edgeproto.Version, HostKey: signer.PublicKey().Marshal(), Signature: sig, AgentVersion: "test", Name: "devbox"}
}

// enroll connects a server with signer and waits for ready.
func enroll(t *testing.T, e *env, signer ssh.Signer) *agent {
	t.Helper()
	ws := dialControl(t, e)
	c := challenge(t, ws)
	send(t, ws, helloFor(t, signer, e.origin, c.Nonce))
	m, err := recv(t.Context(), ws)
	if err != nil {
		t.Fatal(err)
	}
	ready, ok := m.(edgeproto.Ready)
	if !ok {
		t.Fatalf("got %T, want ready", m)
	}
	return &agent{t: t, e: e, signer: signer, id: ready.ServerID, ws: ws, ready: ready,
		msgs: make(chan edgeproto.Message, 64), data: make(chan net.Conn, 64)}
}

// claimedAgent enrolls a claimed server that attaches every open.
func claimedAgent(t *testing.T, e *env) *agent {
	t.Helper()
	signer := newSigner(t)
	id := edgeproto.ServerID(signer.PublicKey())
	e.dir.mu.Lock()
	e.dir.owners[id] = true
	e.dir.mu.Unlock()
	a := enroll(t, e, signer)
	a.attach = true
	go a.run()
	return a
}

func (a *agent) run() {
	for {
		m, err := readControl(context.Background(), a.ws)
		if errors.Is(err, edgeproto.ErrUnknownMessage) {
			continue
		}
		if err != nil {
			close(a.msgs)
			return
		}
		switch m := m.(type) {
		case edgeproto.Ping:
			_ = writeControl(context.Background(), a.ws, edgeproto.Pong{})
			continue
		case edgeproto.Open:
			if a.attach {
				_ = writeControl(context.Background(), a.ws, edgeproto.OpenResult{ConnID: m.ConnID})
				if nc, err := a.dialData(m.ConnID, m.Ticket); err == nil {
					a.data <- nc
				}
			}
		}
		a.msgs <- m
	}
}

func (a *agent) dialData(connID, ticket string) (net.Conn, error) {
	ws, resp, err := websocket.Dial(context.Background(), a.e.wsBase+edgeproto.DataPath(connID), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + ticket}},
	})
	if err != nil {
		if resp != nil {
			return nil, errors.New(resp.Status)
		}
		return nil, err
	}
	return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
}

// next returns the next message the agent received that is of type T.
func next[T edgeproto.Message](t *testing.T, a *agent) T {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-a.msgs:
			if !ok {
				t.Fatalf("control channel closed waiting for %T", *new(T))
			}
			if v, ok := m.(T); ok {
				return v
			}
		case <-timeout:
			t.Fatalf("no %T within 5s", *new(T))
		}
	}
}

func (a *agent) nextData(t *testing.T) net.Conn {
	t.Helper()
	select {
	case nc := <-a.data:
		t.Cleanup(func() { _ = nc.Close() })
		return nc
	case <-time.After(5 * time.Second):
		t.Fatal("no data socket within 5s")
		return nil
	}
}

// connect opens a relayed connection as a client and returns both ends.
func (e *env) connect(t *testing.T, a *agent, token string) (client, server net.Conn) {
	t.Helper()
	ws, _, err := websocket.Dial(t.Context(), e.wsBase+edgeproto.ConnectPath(a.id), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client = websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	t.Cleanup(func() { _ = client.Close() })
	return client, a.nextData(t)
}

// get requests the connect endpoint without upgrading and returns the
// refusal.
func (e *env) get(t *testing.T, serverID string, header http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.base+edgeproto.ConnectPath(serverID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body edgeproto.ErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("status %d: decode body: %v", resp.StatusCode, err)
	}
	return resp.StatusCode, body.Error
}

func bearerHeader(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}}
}

// waitClosed reads ws until the relay closes it and returns the error
// that ended it.
func waitClosed(t *testing.T, ws *websocket.Conn) error {
	t.Helper()
	for {
		_, err := recv(t.Context(), ws)
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("control channel still open after 5s")
		}
		if err != nil && !errors.Is(err, edgeproto.ErrUnknownMessage) {
			return err
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not true within 5s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sameAccount(a, b edgeproto.Account) bool {
	return a.Provider == b.Provider && a.Subject == b.Subject
}
