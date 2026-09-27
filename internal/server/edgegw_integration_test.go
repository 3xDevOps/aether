//go:build integration

package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edge"
	"github.com/3xDevOps/Aether/internal/edge/relay"
	"github.com/3xDevOps/Aether/internal/edgeagent"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// TestIntegrationEdgeDashboardFailuresKeepServerRunning starts a server
// against a real edge whose ACME directory cannot issue: the server learns
// the edge's server domain when it enrolls, asks the CA nothing while the
// edge would refuse every validation because the server is unclaimed,
// starts issuance once claimed, retries the failure in the background, and
// keeps serving SSH through an edge restart until it is stopped, then
// stops cleanly.
func TestIntegrationEdgeDashboardFailuresKeepServerRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The edge verifies enrollment against its own origin, so it needs
	// its address before it starts.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := edge.New(edge.Config{
		DataDir: t.TempDir(), Origin: "http://" + ln.Addr().String(), ServerDomain: "servers.example.test",
		GitHub: &edge.OAuthApp{ClientID: "fake-client-id", ClientSecret: "fake-client-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close() //nolint:errcheck // test edge
	rl, err := relay.New(ctx, svc.RelayConfig(0))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLink(rl)
	mux := http.NewServeMux()
	rl.Register(mux)
	mux.Handle("/", svc.Handler())
	edgeSrv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}}
	edgeSrv.Start()
	defer edgeSrv.Close()
	var asked atomic.Int32
	ca := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		http.Error(w, "ACME directory unavailable", http.StatusInternalServerError)
	}))
	defer ca.Close()

	rt, _, verifyNoLeaks := pickRuntime(t)
	cfg := Config{
		DataDir: filepath.Join(t.TempDir(), "data"), Addr: "127.0.0.1:0", Runtime: rt,
		EdgeURL: edgeSrv.URL, EdgeACMEDirectory: ca.URL,
	}
	srv, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	state, err := edgeagent.OpenState(cfg.DataDir, edgeSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for st, _, _ := state.Status(); !st.Connected; st, _, _ = state.Status() {
		if time.Now().After(deadline) {
			t.Fatal("the server never enrolled")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(time.Second)
	if n := asked.Load(); n != 0 {
		t.Fatalf("the unclaimed server asked the CA %d times; the edge refuses every validation until it is claimed", n)
	}
	code, _, err := state.IssueClaimCode(srv.edge.ServerID(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	owner := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo"}
	if _, _, err := rl.Claim(ctx, code, owner, edgeproto.Device{ID: "dev-1", Label: "laptop"},
		func(context.Context, string, string) error { return nil }); err != nil {
		t.Fatalf("claim: %v", err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for asked.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("certificate issuance never started after the server was claimed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	shutdown, cancelShutdown := context.WithTimeout(ctx, 10*time.Second)
	defer cancelShutdown()
	if err := rl.Shutdown(shutdown); err != nil {
		t.Fatalf("edge shutdown: %v", err)
	}
	select {
	case err := <-runDone:
		t.Fatalf("server stopped after the edge and certificate failures: %v", err)
	case <-time.After(2 * time.Second):
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("SSH listener after the failures: %v", err)
	}
	_ = conn.Close()

	stop()
	if err := <-runDone; err != nil {
		t.Fatalf("Run: %v", err)
	}
	verifyNoLeaks(t)
}
