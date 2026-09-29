package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/edge/relay"
)

// metricsHandler serves the relay's counters in the Prometheus text
// format, and /healthz for aether-edge healthcheck.
func metricsHandler(rl *relay.Relay) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, rl.Metrics())
	})
	return mux
}

// healthcheck asks the edge on this machine for /healthz on its metrics
// listener. That listener is loopback only and answers in every listen
// mode without a certificate, a host name or a forwarded address.
func healthcheck(args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("aether-edge healthcheck", flag.ContinueOnError)
	addr := fs.String("metrics-listen", metricsListenDefault(getenv), "the --metrics-listen of the edge to check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := checkLoopback("--metrics-listen", *addr); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + *addr + "/healthz")
	if err != nil {
		return fmt.Errorf("healthcheck: no edge answers on its metrics listener: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: GET http://%s/healthz answered %s", *addr, resp.Status)
	}
	return nil
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func writeMetrics(w io.Writer, m relay.Metrics) {
	throttled := 0
	if m.Throttled {
		throttled = 1
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
# HELP aether_edge_refusals_total Refused connections and enrollments by reason.
# TYPE aether_edge_refusals_total counter
`, m.Servers, m.UnclaimedServers, m.Splices, m.BytesRelayed, m.EgressThisMonth, throttled)
	reasons := make([]string, 0, len(m.Refusals))
	for r := range m.Refusals {
		reasons = append(reasons, r)
	}
	slices.Sort(reasons)
	for _, r := range reasons {
		_, _ = fmt.Fprintf(w, "aether_edge_refusals_total{reason=\"%s\"} %d\n", labelEscaper.Replace(r), m.Refusals[r])
	}
}
