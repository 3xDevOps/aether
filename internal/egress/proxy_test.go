package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

type fakeNetwork map[netip.Addr]string

func (fakeNetwork) EgressGateway(context.Context, bool) (netip.Addr, error) {
	return netip.Addr{}, runtime.ErrNotFound
}

func (f fakeNetwork) EgressCreationKey(_ context.Context, ip netip.Addr) (string, error) {
	if key, ok := f[ip]; ok {
		return key, nil
	}
	return "", runtime.ErrNotFound
}

type fakeStore struct {
	mu  sync.Mutex
	run domain.Run
	ws  domain.Workspace
}

func (f *fakeStore) setHosts(hosts []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ws.Environment.Egress.Hosts = hosts
}

func (f *fakeStore) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	if id != f.run.ID {
		return nil, store.ErrNotFound
	}
	run := f.run
	return &run, nil
}

func (f *fakeStore) GetWorkspace(_ context.Context, id domain.WorkspaceID) (*domain.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.ws.ID {
		return nil, store.ErrNotFound
	}
	ws := f.ws
	return &ws, nil
}

type fakeBus struct {
	mu     sync.Mutex
	events []events.Event
}

func (b *fakeBus) Publish(_ context.Context, e events.Event) (events.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return e, nil
}

func (b *fakeBus) notes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var notes []string
	for _, e := range b.events {
		notes = append(notes, string(e.RunID)+": "+e.Payload.(events.TimelinePayload).Message)
	}
	return notes
}

type testProxy struct {
	bus   *fakeBus
	store *fakeStore
	proxy *url.URL
	port  string
}

// newTestProxy serves the proxy on loopback to a run at 127.0.0.1 whose
// workspace allows allowed.test and *.wild.test. Every name but
// private.wild.test resolves to an upstream that answers with the path, or
// for /stream sends one line and holds the response open. slow.wild.test
// lists an address that never answers first.
func newTestProxy(t *testing.T, network fakeNetwork) testProxy {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream "+r.URL.Path+"\n")
		if r.URL.Path == "/stream" {
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
		}
	}))
	t.Cleanup(upstream.Close)
	upstreamAddr := netip.MustParseAddrPort(upstream.Listener.Addr().String())
	bus := &fakeBus{}
	st := &fakeStore{
		run: domain.Run{ID: "run-1", WorkspaceID: "ws-1"},
		ws: domain.Workspace{ID: "ws-1", Environment: domain.WorkspaceEnvironment{
			Egress: domain.EgressPolicy{On: true, Hosts: []string{"allowed.test", "*.wild.test"}},
		}},
	}
	p := New(network, st, bus)
	p.recheck = 10 * time.Millisecond
	p.lookup = func(_ context.Context, host string) ([]netip.Addr, []netip.Addr, error) {
		switch host {
		case "private.wild.test":
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil, nil
		case "slow.wild.test":
			addrs := []netip.Addr{netip.MustParseAddr("192.0.2.1"), upstreamAddr.Addr()}
			return addrs, addrs, nil
		}
		return []netip.Addr{upstreamAddr.Addr()}, []netip.Addr{upstreamAddr.Addr()}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	proxy, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return testProxy{bus: bus, store: st, proxy: proxy, port: fmt.Sprint(upstreamAddr.Port())}
}

// connect sends CONNECT target and returns the proxy's answer and the
// connection, which carries the tunnel after a 200.
func (tp testProxy) connect(t *testing.T, target string) (*http.Response, string, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", tp.proxy.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT %s: %v", target, err)
	}
	var body string
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		body = string(data)
	}
	return resp, body, conn, reader
}

func (tp testProxy) client() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(tp.proxy)}}
}

func TestProxyForwardsAllowedHosts(t *testing.T) {
	tp := newTestProxy(t, fakeNetwork{netip.MustParseAddr("127.0.0.1"): "run-1"})

	resp, body, conn, reader := tp.connect(t, "allowed.test:"+tp.port)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT allowed.test = %s %q, want 200", resp.Status, body)
	}
	if _, err := io.WriteString(conn, "GET /tunnel HTTP/1.1\r\nHost: allowed.test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	tunneled, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read through the tunnel: %v", err)
	}
	if got, _ := io.ReadAll(tunneled.Body); string(got) != "upstream /tunnel\n" {
		t.Fatalf("through the tunnel = %q, want %q", got, "upstream /tunnel\n")
	}

	plain, err := tp.client().Get("http://api.wild.test:" + tp.port + "/plain")
	if err != nil {
		t.Fatalf("GET http://api.wild.test through the proxy: %v", err)
	}
	defer func() { _ = plain.Body.Close() }()
	if got, _ := io.ReadAll(plain.Body); plain.StatusCode != http.StatusOK || string(got) != "upstream /plain\n" {
		t.Fatalf("GET http://api.wild.test = %s %q, want 200 %q", plain.Status, got, "upstream /plain\n")
	}
	started := time.Now()
	slow, err := tp.client().Get("http://slow.wild.test:" + tp.port + "/slow")
	if err != nil {
		t.Fatalf("GET http://slow.wild.test through the proxy: %v", err)
	}
	_ = slow.Body.Close()
	if elapsed := time.Since(started); slow.StatusCode != http.StatusOK || elapsed > 5*time.Second {
		t.Fatalf("GET http://slow.wild.test = %s after %s; an address that never answers must not hold up the next", slow.Status, elapsed)
	}
	if notes := tp.bus.notes(); len(notes) != 0 {
		t.Fatalf("timeline notes = %q, want none", notes)
	}
}

// A client may send its whole request and half-close before the answer, as
// `printf ... | nc` does; Go then cancels the request's context.
func TestProxyAnswersAClientThatHalfCloses(t *testing.T) {
	tp := newTestProxy(t, fakeNetwork{netip.MustParseAddr("127.0.0.1"): "run-1"})
	for target, want := range map[string]string{
		"allowed.test:" + tp.port: "upstream /tunnel",
		"denied.test:443":         "egress to denied.test denied",
	} {
		conn, err := net.Dial("tcp", tp.proxy.Host)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nGET /tunnel HTTP/1.0\r\nHost: allowed.test\r\n\r\n", target, target)
		if err == nil {
			err = conn.(*net.TCPConn).CloseWrite()
		}
		if err == nil {
			err = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(conn)
		_ = conn.Close()
		if err != nil || !strings.Contains(string(got), want) {
			t.Errorf("CONNECT %s from a half-closed client = %q, %v; want %q in it", target, got, err, want)
		}
	}
}

func TestProxyClosesTunnelsToDeniedHosts(t *testing.T) {
	tp := newTestProxy(t, fakeNetwork{netip.MustParseAddr("127.0.0.1"): "run-1"})
	resp, body, conn, reader := tp.connect(t, "allowed.test:"+tp.port)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT allowed.test = %s %q, want 200", resp.Status, body)
	}
	stream, err := tp.client().Get("http://allowed.test:" + tp.port + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	streamed := bufio.NewReader(stream.Body)
	if line, readErr := streamed.ReadString('\n'); readErr != nil || line != "upstream /stream\n" {
		t.Fatalf("first streamed line = %q, %v", line, readErr)
	}

	tp.store.setHosts([]string{"*.wild.test"})
	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("read from a tunnel whose host was denied = %v, want EOF", err)
	}
	ended := make(chan error, 1)
	go func() {
		_, readErr := streamed.ReadString('\n')
		ended <- readErr
	}()
	select {
	case err = <-ended:
		if err == nil {
			t.Fatal("a forwarded response whose host was denied sent another line")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a forwarded response whose host was denied is still open")
	}
}

func TestProxyDeniesOtherHosts(t *testing.T) {
	tp := newTestProxy(t, fakeNetwork{netip.MustParseAddr("127.0.0.1"): "run-1"})
	const denied = "aether: egress to denied.test denied; allow it with aether workspace env egress allow --workspace ws-1 denied.test"

	for range 2 {
		resp, body, _, _ := tp.connect(t, "denied.test:443")
		if resp.StatusCode != http.StatusForbidden || resp.Status != "403 "+denied || body != denied+"\n" {
			t.Fatalf("CONNECT denied.test = %q with body %q, want 403 and %q in both", resp.Status, body, denied)
		}
	}
	if _, err := tp.client().Get("https://denied.test/"); err == nil || !strings.Contains(err.Error(), denied) {
		t.Fatalf("Go client error = %v, want it to carry %q", err, denied)
	}
	plain, err := tp.client().Get("http://denied.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Body.Close() }()
	if got, _ := io.ReadAll(plain.Body); plain.StatusCode != http.StatusForbidden || string(got) != denied+"\n" {
		t.Fatalf("GET http://denied.test = %s %q, want 403 %q", plain.Status, got, denied)
	}
	if notes := tp.bus.notes(); len(notes) != 1 || notes[0] != "run-1: "+denied {
		t.Fatalf("timeline notes = %q, want one: %q", notes, denied)
	}

	resp, body, _, _ := tp.connect(t, "wild.test:443")
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "egress to wild.test denied") {
		t.Fatalf("CONNECT wild.test = %s %q; *.wild.test must not match wild.test", resp.Status, body)
	}
	resp, body, _, _ = tp.connect(t, "private.wild.test:443")
	if want := "aether: egress to private.wild.test denied: it resolves to 10.0.0.5"; resp.StatusCode != http.StatusForbidden || !strings.HasPrefix(body, want) {
		t.Fatalf("CONNECT private.wild.test = %s %q, want 403 starting %q", resp.Status, body, want)
	}

	for i := range maxReportedHosts + 5 {
		tp.connect(t, fmt.Sprintf("flood-%d.test:443", i))
	}
	if notes := tp.bus.notes(); len(notes) != maxReportedHosts {
		t.Fatalf("%d timeline notes after many denied hosts, want %d", len(notes), maxReportedHosts)
	}
}

func TestProxyLimitsAnIsolatedRunToAgentHosts(t *testing.T) {
	tp := newTestProxy(t, fakeNetwork{netip.MustParseAddr("127.0.0.1"): "run-1"})
	tp.store.run.Permission = domain.PermissionIsolated

	const denied = "aether: egress to allowed.test denied; an isolated session reaches only the agents' own hosts"
	if resp, body, _, _ := tp.connect(t, "allowed.test:"+tp.port); resp.StatusCode != http.StatusForbidden || body != denied+"\n" {
		t.Fatalf("CONNECT allowed.test from an isolated session = %s %q, want 403 %q", resp.Status, body, denied)
	}
	if resp, body, _, _ := tp.connect(t, "api.anthropic.com:"+tp.port); resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT api.anthropic.com from an isolated session = %s %q, want 200", resp.Status, body)
	}
}

func TestProxyRefusesUnknownSources(t *testing.T) {
	tp := newTestProxy(t, fakeNetwork{})
	resp, body, _, _ := tp.connect(t, "allowed.test:"+tp.port)
	if want := "aether: 127.0.0.1 is not a session with egress control on\n"; resp.StatusCode != http.StatusForbidden || body != want {
		t.Fatalf("CONNECT from an unknown source = %s %q, want 403 %q", resp.Status, body, want)
	}
}

func TestPublicAddrs(t *testing.T) {
	var in []netip.Addr
	for _, s := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "100.64.0.1", "0.0.0.0",
		"::1", "fe80::1", "fd00:ec2::254", "::ffff:10.0.0.1", "224.0.0.1",
		"8.8.8.8", "2606:4700:4700::1111", "::ffff:1.1.1.1",
	} {
		in = append(in, netip.MustParseAddr(s))
	}
	got, err := publicAddrs(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "8.8.8.8, 2606:4700:4700::1111, 1.1.1.1"; joinAddrs(got) != want {
		t.Fatalf("publicAddrs = %s, want %s", joinAddrs(got), want)
	}
}

func TestHosts(t *testing.T) {
	for entry, want := range map[string]string{
		"GitHub.com.":    "github.com",
		" *.Example.org": "*.example.org",
		"1.2.3.4":        "1.2.3.4",
		"https://x.org":  "",
		"x.org:443":      "",
		"*":              "",
		"a.*.org":        "",
		"":               "",
		"a..org":         "",
	} {
		got, err := NormalizeHost(entry)
		if want == "" {
			if err == nil {
				t.Errorf("NormalizeHost(%q) = %q, want an error", entry, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", entry, got, err, want)
		}
	}
	hosts := []string{"github.com", "*.example.org"}
	for host, want := range map[string]bool{
		"github.com": true, "api.github.com": false, "a.example.org": true, "a.b.example.org": true,
		"example.org": false, "badexample.org": false,
	} {
		if got := allowed(hosts, host); got != want {
			t.Errorf("allowed(%q) = %t, want %t", host, got, want)
		}
	}
	for _, host := range Defaults {
		if got, err := NormalizeHost(host); err != nil || got != host {
			t.Errorf("default host %q normalizes to %q, %v", host, got, err)
		}
	}
}
