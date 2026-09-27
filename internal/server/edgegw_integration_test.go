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
)

// TestIntegrationEdgeDashboardFailuresKeepServerRunning starts a server
// against a real edge whose ACME directory cannot issue: the server learns
// the edge's server domain when it enrolls, the edge dashboard starts
// issuance on its own, the failure is retried in the background, and the
// server keeps serving SSH through an edge restart until it is stopped,
// then stops cleanly.
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
	srv, err := New(ctx, Config{
		DataDir: filepath.Join(t.TempDir(), "data"), Addr: "127.0.0.1:0", Runtime: rt,
		EdgeURL: edgeSrv.URL, EdgeACMEDirectory: ca.URL,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	deadline := time.Now().Add(20 * time.Second)
	for asked.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("certificate issuance never started after the server enrolled")
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
