package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type Network interface {
	EgressGateway(ctx context.Context, create bool) (netip.Addr, error)
	EgressCreationKey(ctx context.Context, ip netip.Addr) (string, error)
}

type Store interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error)
}

type Publisher interface {
	Publish(ctx context.Context, e events.Event) (events.Event, error)
}

// maxReportedHosts bounds the denials one run adds to its timeline, so an
// agent that tries many hosts cannot flood it.
const maxReportedHosts = 20

// maxReportedRuns bounds the runs whose reported hosts the proxy remembers.
// Past it the proxy forgets them all, so a run may note a host twice.
const maxReportedRuns = 1024

// recheckInterval is how soon an open tunnel closes after an admin removes
// its host from the list.
const recheckInterval = 10 * time.Second

const dialTimeout = 30 * time.Second

// fallbackDelay is how long an address that has not answered, such as an IPv6
// one the server has no working route to, holds up the next address.
const fallbackDelay = 300 * time.Millisecond

var (
	// sharedAddressSpace (RFC 6598) holds tailnet addresses and Alibaba
	// Cloud's metadata service.
	sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork        = netip.MustParsePrefix("0.0.0.0/8")

	errUnknownSource = errors.New("not a run with egress control on")
)

type addrsKey struct{}

// Proxy reads the host list on every request, so a change applies to
// running runs at once.
type Proxy struct {
	network   Network
	store     Store
	bus       Publisher
	forwarder *httputil.ReverseProxy
	lookup    func(ctx context.Context, host string) (resolved, reachable []netip.Addr, err error)
	recheck   time.Duration

	mu       sync.Mutex
	server   *http.Server
	addr     netip.AddrPort
	reported map[domain.RunID]map[string]bool
}

func New(network Network, store Store, bus Publisher) *Proxy {
	p := &Proxy{
		network:  network,
		store:    store,
		bus:      bus,
		lookup:   lookup,
		recheck:  recheckInterval,
		reported: map[domain.RunID]map[string]bool{},
	}
	p.forwarder = &httputil.ReverseProxy{
		Rewrite: func(*httputil.ProxyRequest) {},
		Transport: &http.Transport{
			DialContext:       dialChecked,
			DisableKeepAlives: true,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, fmt.Sprintf("aether: forward to %s: %v", r.URL.Host, err), http.StatusBadGateway)
		},
	}
	return p
}

func (p *Proxy) Prepare(ctx context.Context) (map[string]string, error) {
	gateway, err := p.network.EgressGateway(ctx, true)
	if err != nil {
		return nil, err
	}
	addr, err := p.listen(gateway)
	if err != nil {
		return nil, err
	}
	return Env(addr), nil
}

// Resume serves the runs that kept their containers across a server restart.
// Without an egress network there are none.
func (p *Proxy) Resume(ctx context.Context) error {
	gateway, err := p.network.EgressGateway(ctx, false)
	if errors.Is(err, runtime.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = p.listen(gateway)
	return err
}

func (p *Proxy) listen(gateway netip.Addr) (netip.AddrPort, error) {
	addr := netip.AddrPortFrom(gateway, Port)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil && p.addr == addr {
		return addr, nil
	}
	ln, err := net.Listen("tcp", addr.String())
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("egress: listen on the %s gateway: %w", runtime.EgressNetwork, err)
	}
	if p.server != nil {
		// The network was recreated on another subnet, which only happens
		// once every container on the old one is gone.
		_ = p.server.Close()
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(p.serve),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	p.server, p.addr = server, addr
	go func() {
		if serveErr := server.Serve(ln); !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("egress: proxy stopped", "addr", addr, "error", serveErr)
		}
	}()
	return addr, nil
}

func (p *Proxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server == nil {
		return nil
	}
	return p.server.Close()
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if (r.Method != http.MethodConnect && r.URL.Scheme != "http") || r.URL.Host == "" {
		refuse(w, r, http.StatusBadRequest, "aether: the egress proxy takes CONNECT and absolute http:// URLs")
		return
	}
	host := strings.TrimSuffix(strings.ToLower(r.URL.Hostname()), ".")
	port := r.URL.Port()
	if port == "" {
		port = "80"
		if r.Method == http.MethodConnect {
			port = "443"
		}
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		refuse(w, r, http.StatusBadRequest, fmt.Sprintf("aether: %q is not a port", port))
		return
	}
	// A client that half-closes after its CONNECT cancels r.Context(); it
	// still gets an answer.
	ctx := context.WithoutCancel(r.Context())
	run, ws, err := p.source(ctx, r.RemoteAddr)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errUnknownSource) {
			status = http.StatusForbidden
		}
		refuse(w, r, status, "aether: "+err.Error())
		return
	}
	if !allowed(runHosts(run, ws), host) {
		msg := fmt.Sprintf("aether: egress to %s denied; allow it with aether workspace env egress allow --workspace %s %s", host, ws.ID, host)
		if run.Permission == domain.PermissionIsolated {
			msg = fmt.Sprintf("aether: egress to %s denied; an isolated run reaches only the agents' own hosts", host)
		}
		p.deny(w, r, run, host, msg)
		return
	}
	resolved, addrs, err := p.lookup(ctx, host)
	if err != nil {
		refuse(w, r, http.StatusBadGateway, "aether: "+err.Error())
		return
	}
	if len(addrs) == 0 {
		p.deny(w, r, run, host, fmt.Sprintf("aether: egress to %s denied: it resolves to %s, and the proxy never connects to loopback, private, link-local or this server's addresses", host, joinAddrs(resolved)))
		return
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, r.WithContext(context.WithValue(ctx, addrsKey{}, addrs)), run, host, port)
		return
	}
	// A forwarded request still ends when its client goes away.
	forward, cancel := context.WithCancel(context.WithValue(r.Context(), addrsKey{}, addrs))
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go p.closeWhenDenied(done, run, host, cancel)
	p.forwarder.ServeHTTP(w, r.WithContext(forward))
}

func (p *Proxy) source(ctx context.Context, remote string) (*domain.Run, *domain.Workspace, error) {
	addr, err := netip.ParseAddrPort(remote)
	if err != nil {
		return nil, nil, fmt.Errorf("parse the source address %q: %w", remote, err)
	}
	ip := addr.Addr().Unmap()
	key, err := p.network.EgressCreationKey(ctx, ip)
	if errors.Is(err, runtime.ErrNotFound) {
		return nil, nil, fmt.Errorf("%s is %w", ip, errUnknownSource)
	}
	if err != nil {
		return nil, nil, err
	}
	// The scheduler creates a run's container with the run ID as its
	// creation key.
	run, err := p.store.GetRun(ctx, domain.RunID(key))
	if err != nil {
		return nil, nil, fmt.Errorf("look up run %s: %w", key, err)
	}
	ws, err := p.store.GetWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("look up workspace %s: %w", run.WorkspaceID, err)
	}
	return run, ws, nil
}

func (p *Proxy) deny(w http.ResponseWriter, r *http.Request, run *domain.Run, host, msg string) {
	p.mu.Lock()
	hosts := p.reported[run.ID]
	if hosts == nil {
		if len(p.reported) >= maxReportedRuns {
			clear(p.reported)
		}
		hosts = map[string]bool{}
		p.reported[run.ID] = hosts
	}
	first := !hosts[host] && len(hosts) < maxReportedHosts
	if first {
		hosts[host] = true
	}
	p.mu.Unlock()
	if first {
		if _, err := p.bus.Publish(context.WithoutCancel(r.Context()), events.Event{
			WorkspaceID: run.WorkspaceID,
			RunID:       run.ID,
			Payload:     events.TimelinePayload{Kind: events.TimelineNote, Message: msg},
		}); err != nil {
			slog.Warn("egress: record a denial on the run timeline", "run", run.ID, "host", host, "error", err)
		}
	}
	refuse(w, r, http.StatusForbidden, msg)
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request, run *domain.Run, host, port string) {
	upstream, err := dialChecked(r.Context(), "tcp", net.JoinHostPort(r.URL.Hostname(), port))
	if err != nil {
		refuse(w, r, http.StatusBadGateway, fmt.Sprintf("aether: connect to %s: %v", r.URL.Host, err))
		return
	}
	defer func() { _ = upstream.Close() }()
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "aether: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = conn.Close() }()
	if conn.SetDeadline(time.Time{}) != nil {
		return
	}
	if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	closeBoth := func() {
		_ = upstream.Close()
		_ = conn.Close()
	}
	done := make(chan struct{})
	defer close(done)
	go p.closeWhenDenied(done, run, host, closeBoth)
	go func() {
		_, copyErr := io.Copy(upstream, buffered.Reader)
		if copyErr == nil {
			if tcp, ok := upstream.(*net.TCPConn); ok {
				copyErr = tcp.CloseWrite()
			}
		}
		if copyErr != nil {
			closeBoth()
		}
	}()
	_, _ = io.Copy(conn, upstream)
}

// closeWhenDenied ends a connection once its host leaves the run's list, or
// once the list cannot be read.
func (p *Proxy) closeWhenDenied(done <-chan struct{}, run *domain.Run, host string, end func()) {
	ticker := time.NewTicker(p.recheck)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ws, err := p.store.GetWorkspace(context.Background(), run.WorkspaceID)
			if err != nil || !allowed(runHosts(run, ws), host) {
				end()
				return
			}
		}
	}
}

// dialChecked connects only to the addresses serve resolved and checked for
// this request, so a name cannot resolve again to a different address.
func dialChecked(ctx context.Context, network, address string) (net.Conn, error) {
	addrs, _ := ctx.Value(addrsKey{}).([]netip.Addr)
	_, port, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		return nil, splitErr
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no checked address for %s", address)
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	type attempt struct {
		conn net.Conn
		err  error
	}
	attempts := make(chan attempt, len(addrs))
	for i, addr := range addrs {
		go func() {
			select {
			case <-time.After(time.Duration(i) * fallbackDelay):
			case <-ctx.Done():
				attempts <- attempt{err: ctx.Err()}
				return
			}
			var dialer net.Dialer
			c, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			attempts <- attempt{c, err}
		}()
	}
	var conn net.Conn
	var errs []error
	for range addrs {
		a := <-attempts
		switch {
		case a.err != nil:
			errs = append(errs, a.err)
		case conn == nil:
			conn = a.conn
			cancel()
		default:
			_ = a.conn.Close()
		}
	}
	if conn != nil {
		return conn, nil
	}
	return nil, errors.Join(errs...)
}

// refuse puts msg in the status line of a refused CONNECT as well, because
// many clients show only that line when a tunnel fails.
func refuse(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.Method != http.MethodConnect {
		http.Error(w, msg, status)
		return
	}
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, msg, status)
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s\n", status, msg, len(msg)+1, msg)
}

func lookup(ctx context.Context, host string) (resolved, reachable []netip.Addr, err error) {
	if resolved, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	reachable, err = publicAddrs(resolved)
	return resolved, reachable, err
}

// publicAddrs drops the addresses an allowed name must not lead to: this
// server, its LANs and tailnets, and cloud metadata services.
func publicAddrs(addrs []netip.Addr) ([]netip.Addr, error) {
	own, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("list this server's addresses: %w", err)
	}
	local := make(map[netip.Addr]bool, len(own))
	for _, a := range own {
		if prefix, parseErr := netip.ParsePrefix(a.String()); parseErr == nil {
			local[prefix.Addr().Unmap()] = true
		}
	}
	var public []netip.Addr
	for _, addr := range addrs {
		addr = addr.Unmap()
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() ||
			sharedAddressSpace.Contains(addr) || thisNetwork.Contains(addr) || local[addr] {
			continue
		}
		public = append(public, addr)
	}
	return public, nil
}

func joinAddrs(addrs []netip.Addr) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.Unmap().String()
	}
	return strings.Join(s, ", ")
}
