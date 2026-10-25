//go:build integration

package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// A run on the egress network reaches an allowed host only through the
// proxy on the network's gateway, gets the denial for any other host, and has
// no route out of its own.
func TestEgressRunReachesOnlyAllowedHosts(t *testing.T) {
	d, err := runtime.NewDocker(runtime.WithLabels(map[string]string{"aether.test": t.Name()}))
	if err != nil {
		t.Fatalf("NewDocker() error: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := t.Context()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream "+r.URL.Path)
	}))
	t.Cleanup(upstream.Close)
	upstreamAddr := netip.MustParseAddrPort(upstream.Listener.Addr().String())

	const key = "egress-integration-run"
	bus := &fakeBus{}
	p := New(d, &fakeStore{
		run: domain.Run{ID: key, WorkspaceID: "ws-1"},
		ws: domain.Workspace{ID: "ws-1", Environment: domain.WorkspaceEnvironment{
			Egress: domain.EgressPolicy{On: true, Hosts: []string{"allowed.test"}},
		}},
	}, bus)
	// The test's upstream is on loopback, which the real lookup refuses.
	p.lookup = func(context.Context, string) ([]netip.Addr, []netip.Addr, error) {
		return []netip.Addr{upstreamAddr.Addr()}, []netip.Addr{upstreamAddr.Addr()}, nil
	}
	t.Cleanup(func() { _ = p.Close() })
	env, err := p.Prepare(ctx)
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}
	gateway, gatewayPort, err := net.SplitHostPort(strings.TrimPrefix(env["HTTP_PROXY"], "http://"))
	if err != nil {
		t.Fatalf("HTTP_PROXY %q: %v", env["HTTP_PROXY"], err)
	}

	id, err := d.Create(ctx, runtime.Spec{
		Image:       "busybox:1.36",
		Command:     []string{"sleep", "600"},
		Env:         env,
		Egress:      true,
		CreationKey: key,
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Destroy(cleanup, id); err != nil {
			t.Errorf("cleanup Destroy() error: %v", err)
		}
	})
	if err = d.Start(ctx, id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	run := func(script string) (int, string) {
		t.Helper()
		code, stdout, stderr, err := d.Exec(ctx, id, []string{"/bin/sh", "-c", script}, "")
		if err != nil {
			t.Fatalf("Exec(%s) error: %v", script, err)
		}
		return code, stdout + stderr
	}

	port := fmt.Sprint(upstreamAddr.Port())
	if code, out := run("timeout 20 wget -q -O - http://allowed.test:" + port + "/plain"); code != 0 || out != "upstream /plain" {
		t.Fatalf("wget through the proxy: exit %d, output %q", code, out)
	}
	tunnel := fmt.Sprintf(`printf 'CONNECT allowed.test:%s HTTP/1.1\r\nHost: allowed.test\r\n\r\nGET /tunnel HTTP/1.0\r\nHost: allowed.test\r\n\r\n' | timeout 20 nc %s %s`, port, gateway, gatewayPort)
	if _, out := run(tunnel); !strings.Contains(out, "200 Connection established") || !strings.HasSuffix(out, "upstream /tunnel") {
		t.Fatalf("CONNECT allowed.test: %q", out)
	}
	const denied = "403 aether: egress to denied.test denied; allow it with aether workspace env egress allow --workspace ws-1 denied.test"
	refused := fmt.Sprintf(`printf 'CONNECT denied.test:443 HTTP/1.1\r\nHost: denied.test:443\r\n\r\n' | timeout 20 nc %s %s`, gateway, gatewayPort)
	if _, out := run(refused); !strings.Contains(out, denied) {
		t.Fatalf("CONNECT denied.test: %q, want %q", out, denied)
	}
	if notes := bus.notes(); len(notes) != 1 || !strings.Contains(notes[0], "egress to denied.test denied") {
		t.Fatalf("timeline notes = %q, want one for denied.test", notes)
	}
	if code, out := run("timeout 10 nc -w 3 1.1.1.1 443 </dev/null"); code == 0 {
		t.Fatalf("direct connection to 1.1.1.1 succeeded: %q", out)
	}
}
