package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/edge/relay"
	edge "github.com/3xDevOps/Aether/internal/edge/service"
)

// TestReadiness follows /readyz and healthcheck --ready through a life:
// starting while a check fails before the edge was ready, ready, unready
// when a check fails after, draining once shutdown starts, and the
// database's own error once it is closed.
func TestReadiness(t *testing.T) {
	svc, err := edge.New(edge.Config{
		DataDir: t.TempDir(), SigninOrigin: "http://localhost:8080", RelayOrigin: "http://127.0.0.1:8080",
		GitHub: &edge.OAuthApp{ClientID: "fake-client-id", ClientSecret: fakeSecret},
	})
	if err != nil {
		t.Fatal(err)
	}
	rl, err := relay.New(context.Background(), svc.RelayConfig(0))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLink(rl)
	var listener listenerState
	srv := httptest.NewServer(metricsHandler(svc, rl, &listener))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	check := func() (readyReport, int, error) {
		t.Helper()
		resp, getErr := http.Get(srv.URL + "/readyz")
		if getErr != nil {
			t.Fatal(getErr)
		}
		defer resp.Body.Close() //nolint:errcheck // read-only
		var rep readyReport
		if decodeErr := json.NewDecoder(resp.Body).Decode(&rep); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		var out bytes.Buffer
		return rep, resp.StatusCode, healthcheck([]string{"--ready", "--metrics-listen", addr}, func(string) string { return "" }, &out)
	}

	reason := "accept tcp: use of closed network connection"
	listener.stopped.Store(&reason)
	rep, code, err := check()
	if code != http.StatusServiceUnavailable || rep.Status != statusStarting || rep.Checks.Listener != reason ||
		err == nil || !strings.Contains(err.Error(), "edge is not ready (starting): listener: "+reason) {
		t.Fatalf("edge never ready: %d %+v, healthcheck --ready: %v", code, rep, err)
	}
	listener.stopped.Store(nil)
	rep, code, err = check()
	if code != http.StatusOK || rep.Status != statusReady || rep.Checks.DB != checkOK || rep.Protocol.Version == 0 || err != nil {
		t.Fatalf("running edge: %d %+v, healthcheck --ready: %v", code, rep, err)
	}
	listener.stopped.Store(&reason)
	if rep, code, _ = check(); code != http.StatusServiceUnavailable || rep.Status != statusUnready {
		t.Fatalf("listener stopped after the edge was ready: %d %+v", code, rep)
	}
	listener.stopped.Store(nil)
	var out bytes.Buffer
	if err = healthcheck([]string{"--json", "--metrics-listen", addr}, func(string) string { return "" }, &out); err != nil || !strings.Contains(out.String(), `"status":"ready"`) {
		t.Fatalf("healthcheck --json: %v\n%s", err, out.String())
	}

	if err = rl.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep, code, err = check()
	if code != http.StatusServiceUnavailable || rep.Status != statusDraining || !rep.Checks.Draining ||
		err == nil || !strings.Contains(err.Error(), "edge is not ready (draining): draining") {
		t.Fatalf("draining edge: %d %+v, healthcheck --ready: %v", code, rep, err)
	}
	if err = healthcheck([]string{"--metrics-listen", addr}, func(string) string { return "" }, &out); err != nil {
		t.Fatalf("liveness of a draining edge: %v", err)
	}

	if err = svc.Close(); err != nil {
		t.Fatal(err)
	}
	rep, _, err = check()
	if !strings.Contains(rep.Checks.DB, "database is closed") || err == nil || !strings.Contains(err.Error(), "db: "+rep.Checks.DB) {
		t.Fatalf("closed database: %+v, healthcheck --ready: %v", rep, err)
	}
}
