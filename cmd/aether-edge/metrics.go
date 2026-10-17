package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/edge/relay"
	edge "github.com/3xDevOps/Aether/internal/edge/service"
	"github.com/3xDevOps/Aether/internal/version"
)

// metricsHandler serves the relay's counters in the Prometheus text
// format, /healthz for aether-edge healthcheck and /readyz for
// aether-edge healthcheck --ready.
func metricsHandler(svc *edge.Service, rl *relay.Relay, listener *listenerState) http.Handler {
	mux := http.NewServeMux()
	var wasReady atomic.Bool
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		rep := readiness(r.Context(), svc, rl.Metrics(), listener.check(), wasReady.Load())
		code := http.StatusOK
		if rep.Status == statusReady {
			wasReady.Store(true)
		} else {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(rep)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, rl.Metrics(), svc.GitHubErrors())
	})
	return mux
}

// listenerState is the public listener's check: "ok" while it serves,
// then why it stopped.
type listenerState struct{ stopped atomic.Pointer[string] }

func (l *listenerState) check() string {
	if s := l.stopped.Load(); s != nil {
		return *s
	}
	return checkOK
}

// serve serves srv on ln and records when it stops.
func (l *listenerState) serve(name string, srv *http.Server, ln net.Listener) error {
	err := serveHTTP(name, srv, ln)
	reason := "closed"
	if err != nil {
		reason = err.Error()
	}
	l.stopped.Store(&reason)
	return err
}

const (
	checkOK        = "ok"
	statusReady    = "ready"
	statusStarting = "starting"
	statusUnready  = "unready"
	statusDraining = "draining"
	// dbCheckTimeout bounds the database check of one /readyz.
	dbCheckTimeout = time.Second
)

// readyReport is the body of /readyz. GitHub and ACME are not checked: an
// edge that cannot reach them still relays every enrolled server.
type readyReport struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Protocol struct {
		Version    int `json:"version"`
		MinVersion int `json:"min_version"`
	} `json:"protocol"`
	Checks struct {
		DB       string `json:"db"`
		Listener string `json:"listener"`
		Draining bool   `json:"draining"`
	} `json:"checks"`
	Servers struct {
		Claimed   int `json:"claimed"`
		Unclaimed int `json:"unclaimed"`
	} `json:"servers"`
}

// readiness reports a failing check as starting until the edge was first
// ready, and as unready after.
func readiness(ctx context.Context, svc *edge.Service, m relay.Metrics, listener string, wasReady bool) readyReport {
	var rep readyReport
	rep.Version = version.Version
	rep.Protocol.Version, rep.Protocol.MinVersion = edgeproto.Version, edgeproto.MinVersion
	rep.Servers.Claimed, rep.Servers.Unclaimed = m.Servers, m.UnclaimedServers
	rep.Checks.Draining, rep.Checks.Listener, rep.Checks.DB = m.Draining, listener, checkOK
	ctx, cancel := context.WithTimeout(ctx, dbCheckTimeout)
	defer cancel()
	if err := svc.Ping(ctx); err != nil {
		rep.Checks.DB = err.Error()
	}
	switch {
	case m.Draining:
		rep.Status = statusDraining
	case rep.Checks.DB == checkOK && rep.Checks.Listener == checkOK:
		rep.Status = statusReady
	case wasReady:
		rep.Status = statusUnready
	default:
		rep.Status = statusStarting
	}
	return rep
}

// healthcheck asks the edge on this machine for /healthz, or /readyz with
// --ready, on its metrics listener. That listener is loopback only and
// answers in every listen mode without a certificate, a host name or a
// forwarded address.
func healthcheck(args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("aether-edge healthcheck", flag.ContinueOnError)
	addr := fs.String("metrics-listen", metricsListenDefault(getenv), "the --metrics-listen of the edge to check")
	ready := fs.Bool("ready", false, "check readiness (/readyz) instead of liveness (/healthz)")
	asJSON := fs.Bool("json", false, "print the /readyz report; implies --ready")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	*ready = *ready || *asJSON
	if err := checkLoopback("--metrics-listen", *addr); err != nil {
		return err
	}
	path := "/healthz"
	if *ready {
		path = "/readyz"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + *addr + path)
	if err != nil {
		return fmt.Errorf("healthcheck: no edge answers on %s: %w; when serve was given --metrics-listen, give this command the same address or set AETHER_EDGE_METRICS_LISTEN for both", *addr, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	if !*ready {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("healthcheck: GET http://%s%s answered %s", *addr, path, resp.Status)
		}
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("healthcheck: GET http://%s%s: %w", *addr, path, err)
	}
	var rep readyReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return fmt.Errorf("healthcheck: GET http://%s%s answered %s: %w", *addr, path, resp.Status, err)
	}
	if *asJSON {
		if _, err := stdout.Write(body); err != nil {
			return err
		}
	}
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var failing []string
	if rep.Checks.Draining {
		failing = append(failing, "draining: the edge is stopping")
	}
	if rep.Checks.DB != checkOK {
		failing = append(failing, "db: "+rep.Checks.DB)
	}
	if rep.Checks.Listener != checkOK {
		failing = append(failing, "listener: "+rep.Checks.Listener)
	}
	return fmt.Errorf("healthcheck: edge is not ready (%s): %s", rep.Status, strings.Join(failing, "; "))
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func writeMetrics(w io.Writer, m relay.Metrics, githubErrors uint64) {
	throttled, draining := 0, 0
	if m.Throttled {
		throttled = 1
	}
	if m.Draining {
		draining = 1
	}
	_, _ = fmt.Fprintf(w, `# HELP aether_edge_servers Servers holding a control channel.
# TYPE aether_edge_servers gauge
aether_edge_servers{state="claimed"} %d
aether_edge_servers{state="unclaimed"} %d
# HELP aether_edge_splices Relayed connections with both ends attached.
# TYPE aether_edge_splices gauge
aether_edge_splices %d
# HELP aether_edge_relayed_bytes_total Bytes relayed since the edge started.
# TYPE aether_edge_relayed_bytes_total counter
aether_edge_relayed_bytes_total %d
# HELP aether_edge_egress_month_bytes Bytes sent this calendar month (UTC).
# TYPE aether_edge_egress_month_bytes gauge
aether_edge_egress_month_bytes %d
# HELP aether_edge_throttled 1 when the monthly egress budget is spent.
# TYPE aether_edge_throttled gauge
aether_edge_throttled %d
# HELP aether_edge_draining 1 once the edge is stopping and draining its servers.
# TYPE aether_edge_draining gauge
aether_edge_draining %d
# HELP aether_edge_github_errors_total Calls to GitHub that failed since the edge started.
# TYPE aether_edge_github_errors_total counter
aether_edge_github_errors_total %d
# HELP aether_edge_servers_by_protocol Servers holding a control channel by negotiated protocol version.
# TYPE aether_edge_servers_by_protocol gauge
`, m.Servers, m.UnclaimedServers, m.Splices, m.BytesRelayed, m.EgressThisMonth, throttled, draining, githubErrors)
	for _, v := range slices.Sorted(maps.Keys(m.ServersByProtocol)) {
		_, _ = fmt.Fprintf(w, "aether_edge_servers_by_protocol{version=\"%d\"} %d\n", v, m.ServersByProtocol[v])
	}
	_, _ = fmt.Fprint(w, `# HELP aether_edge_refusals_total Refused connections and enrollments by reason.
# TYPE aether_edge_refusals_total counter
`)
	reasons := make([]string, 0, len(m.Refusals))
	for r := range m.Refusals {
		reasons = append(reasons, r)
	}
	slices.Sort(reasons)
	for _, r := range reasons {
		_, _ = fmt.Fprintf(w, "aether_edge_refusals_total{reason=\"%s\"} %d\n", labelEscaper.Replace(r), m.Refusals[r])
	}
}
