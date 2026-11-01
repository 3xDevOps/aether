package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/reachability"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/sysbox"
)

// answers builds the stdin an operator would type: one line per prompt, in
// the order askServerOptions asks them, ending with the write confirmation.
// Without Tailscale there are no tailnet questions and the edge is on, so
// the last answer before the confirmation chooses its access policy.
func answers(lines ...string) *strings.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestAskServerOptionsEmptyAnswersTakeDefaults(t *testing.T) {
	var out bytes.Buffer
	in := answers("", "", "2", "yes")
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2222" || values["data-dir"] != "/var/lib/aether" {
		t.Errorf("values = %v, want the service defaults for addr and data-dir", values)
	}
}

func TestAskServerOptionsUsesTypedAnswers(t *testing.T) {
	var out bytes.Buffer
	in := answers(":2300", "/srv/aether", "yes", "true", "true", "", "yes")
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"addr":                ":2300",
		"data-dir":            "/srv/aether",
		"tailnet-auto-join":   "true",
		"tailnet-require-key": "true",
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q, want %q", k, values[k], v)
		}
	}
}

func TestAskServerOptionsSeedsDefaultsFromExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"addr": ":2300", "data-dir": "/srv/aether"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "2", "yes")), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2300" || values["data-dir"] != "/srv/aether" {
		t.Errorf("values = %v, want the existing config as the defaults", values)
	}
}

func TestAskServerOptionsRejectsBadValueAndReasks(t *testing.T) {
	var out bytes.Buffer
	in := answers("", "", "yes", "not-a-bool", "true", "", "", "", "yes")
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
	if err != nil {
		t.Fatal(err)
	}
	if values["tailnet-auto-join"] != "true" {
		t.Errorf("tailnet-auto-join = %q, want the retyped true", values["tailnet-auto-join"])
	}
	if !strings.Contains(out.String(), "answer yes or no; try again") {
		t.Errorf("a rejected answer must be re-asked:\n%s", out.String())
	}
}

func TestAskServerOptionsDeclinedWritesNothing(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "2", "no")), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if values != nil {
		t.Errorf("values = %v, want nil when the operator declines", values)
	}
}

// fakeTailscaled answers the LocalAPI status request the way tailscaled
// does for a node at addrs on the tailnet alice@example.com, reporting a
// certificate name when certs is set, as a tailnet with HTTPS certificates
// enabled does.
func fakeTailscaled(t *testing.T, certs bool, addrs ...string) *reachability.Tailscale {
	t.Helper()
	certDomains := "null"
	if certs {
		certDomains = `["box.tail1234.ts.net"]`
	}
	ips, err := json.Marshal(addrs)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "ts.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"Self":{"DNSName":"box.tail1234.ts.net.","TailscaleIPs":%s},"CertDomains":%s,`+
			`"MagicDNSSuffix":"tail1234.ts.net","CurrentTailnet":{"Name":"alice@example.com"}}`, ips, certDomains)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return reachability.NewTailscale(sock)
}

// startable is a tailnet address that binds port 443 as root and refuses
// it only with EACCES otherwise, so the dashboard can start there either way.
const startable = "127.0.0.1"

// Without Tailscale nothing asks about the tailnet, and auto-join is
// written off for a Tailscale installed later. A key requirement already in
// the config stays.
func TestAskServerOptionsAsksNoTailnetQuestionsWithoutTailscale(t *testing.T) {
	for name, tc := range map[string]struct {
		config, want map[string]string
	}{
		"first run": {nil, map[string]string{"tailnet-auto-join": "false"}},
		"re-run": {map[string]string{"tailnet-auto-join": "true", "tailnet-require-key": "true"},
			map[string]string{"tailnet-auto-join": "false", "tailnet-require-key": "true"}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.conf")
			if tc.config != nil {
				if err := serversetup.WriteConfig(path, tc.config); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			values, err := askServerOptions(newPrompter(&out, answers("", "", "2", "yes")), path, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"tailnet-auto-join", "tailnet-require-key"} {
				if values[key] != tc.want[key] {
					t.Errorf("%s = %q, want %q", key, values[key], tc.want[key])
				}
			}
			for _, asked := range []string{"Will people reach", "Auto-approve", "require pubkey"} {
				if strings.Contains(out.String(), asked) {
					t.Errorf("asked %q without Tailscale:\n%s", asked, out.String())
				}
			}
		})
	}
}

// Input that ends at a prompt writes nothing, even where every question
// left has a default: Ctrl-D is not consent.
func TestAskServerOptionsStopsWhenInputEnds(t *testing.T) {
	for name, input := range map[string]string{"at the first prompt": "", "mid-way": "\n\n", "after an answer with no newline": "\n\nyes"} {
		var out bytes.Buffer
		values, err := askServerOptions(newPrompter(&out, strings.NewReader(input)), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
		if !errors.Is(err, errInputEnded) || values != nil {
			t.Errorf("%s: values %v, err %v; want nothing and errInputEnded", name, values, err)
		}
	}
}

// A first run on a host whose tailscaled names its tailnet takes Tailscale
// on Enter. A no, or a tailnet setup cannot read, turns tailnet sign-in off
// and the edge on, exactly as without Tailscale.
func TestAskServerOptionsPrefersTailscale(t *testing.T) {
	edgeOn := map[string]string{
		"addr":                ":2222",
		"data-dir":            "/var/lib/aether",
		"tailnet-auto-join":   "false",
		"tailnet-require-key": "true",
		"edge-url":            edgeagent.DefaultURL,
		"edge-access":         string(edgeproto.PolicyApprovedDevices),
	}
	named := func(t *testing.T) *reachability.Tailscale { return fakeTailscaled(t, true, startable) }
	for name, tc := range map[string]struct {
		tailscaled func(*testing.T) *reachability.Tailscale
		input      []string
		values     map[string]string
		say        []string
	}{
		"named tailnet, Enter": {named, []string{"", "", "", "", "", "", "", "yes"},
			map[string]string{"addr": ":2222", "data-dir": "/var/lib/aether", "tailnet-auto-join": "false",
				"tailnet-require-key": "false", "web-port": "443", "edge-url": ""},
			[]string{"\nThis host is on the tailnet alice@example.com.\nWith Tailscale, people on it link by this host's name",
				"Use Tailscale to reach this server (yes/no) [yes]: "},
		},
		"named tailnet, no": {named, []string{"", "", "no", "2", "yes"}, edgeOn,
			[]string{"Answer no to use Aether's relay",
				"\nSetup turns on the edge, Aether's relay at https://edge.onaether.dev, run by the Aether project.\n",
				"client IP addresses", `aether-server config set edge-url ""`},
		},
		"unreadable tailnet": {
			func(t *testing.T) *reachability.Tailscale {
				return reachability.NewTailscale(filepath.Join(t.TempDir(), "absent.sock"))
			},
			[]string{"", "", "", "2", "yes"}, edgeOn,
			[]string{"\ntailscaled is installed, but setup cannot read which tailnet it is on: status: ",
				"connect: no such file or directory\n",
				"Use Tailscale to reach this server (yes/no) [no]: "},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			values, err := askServerOptions(newPrompter(&out, answers(tc.input...)), filepath.Join(t.TempDir(), "absent.conf"), tc.tailscaled(t))
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(values, tc.values) {
				t.Errorf("values = %v, want %v", values, tc.values)
			}
			for _, say := range tc.say {
				if !strings.Contains(out.String(), say) {
					t.Errorf("setup does not say %q:\n%s", say, out.String())
				}
			}
			if tc.values["edge-url"] == "" {
				return
			}
			for _, asked := range []string{"Let anyone on the tailnet", "registered SSH key", "Dashboard HTTPS port", "Turn on the edge too"} {
				if strings.Contains(out.String(), asked) {
					t.Errorf("asked %q after a no:\n%s", asked, out.String())
				}
			}
		})
	}
}

// A yes asks the tailnet questions, with auto-join off and the edge off
// unless typed.
func TestAskServerOptionsAsksTheTailnetQuestionsOnYes(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "yes", "", "", "", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"addr":                ":2222",
		"data-dir":            "/var/lib/aether",
		"tailnet-auto-join":   "false",
		"tailnet-require-key": "false",
		"web-port":            "443",
		"edge-url":            "",
	}
	if !maps.Equal(values, want) {
		t.Errorf("values = %v, want %v", values, want)
	}
	for _, say := range []string{"Let anyone on the tailnet join without an admin approving them (yes/no) [no]: ",
		"https://box.tail1234.ts.net/", "Turn on the edge too (yes/no) [no]: "} {
		if !strings.Contains(out.String(), say) {
			t.Errorf("setup does not say %q:\n%s", say, out.String())
		}
	}
}

// A first run defaults the tailnet question to yes, and a re-run only for
// a config that shows tailnet use: the edge off, a web-port, or auto-join on.
func TestAskServerOptionsDefaultsTheTailnetQuestionToTheConfig(t *testing.T) {
	edgeOn := map[string]string{"edge-url": edgeagent.DefaultURL, "edge-access": "approved-devices"}
	with := func(kv ...string) map[string]string {
		config := maps.Clone(edgeOn)
		for i := 0; i < len(kv); i += 2 {
			config[kv[i]] = kv[i+1]
		}
		return config
	}
	for name, tc := range map[string]struct {
		config map[string]string
		want   string
	}{
		"first run":                        {nil, "yes"},
		"what a no writes":                 {with("tailnet-auto-join", "false", "tailnet-require-key", "true"), "no"},
		"written without tailscaled":       {with("tailnet-auto-join", "false"), "no"},
		"written by install with the edge": {with("addr", ":2222"), "no"},
		"key required as 1, the edge on":   {with("tailnet-require-key", "1"), "no"},
		"a web-port":                       {with("tailnet-require-key", "false", "web-port", "0"), "yes"},
		"auto-join on":                     {with("tailnet-auto-join", "1"), "yes"},
		"the edge off":                     {map[string]string{"tailnet-require-key": "true", "edge-url": ""}, "yes"},
		"written by install, no edge-url":  {map[string]string{"addr": ":2222", "data-dir": "/var/lib/aether"}, "yes"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.conf")
			if tc.config != nil {
				if err := serversetup.WriteConfig(path, tc.config); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			_, _ = askServerOptions(newPrompter(&out, answers("", "", "")), path, fakeTailscaled(t, true, startable))
			if want := "Use Tailscale to reach this server (yes/no) [" + tc.want + "]: "; !strings.Contains(out.String(), want) {
				t.Errorf("setup does not ask %q:\n%s", want, out.String())
			}
		})
	}
}

// Pressing Enter through a re-run writes the config the first run wrote,
// whichever way the tailnet question was answered.
func TestAskServerOptionsRerunKeepsTheTailnetAnswer(t *testing.T) {
	for name, tc := range map[string]struct{ first, rerun []string }{
		"no":                   {[]string{"", "", "no", "2", "yes"}, []string{"", "", "", "2", "yes"}},
		"yes":                  {[]string{"", "", "yes", "true", "", "", "", "yes"}, []string{"", "", "", "", "", "", "", "yes"}},
		"yes, a key, the edge": {[]string{"", "", "yes", "", "true", "yes", "2", "yes"}, []string{"", "", "", "2", "yes"}},
		"yes, a key, edge off": {[]string{"", "", "yes", "", "true", "", "yes"}, []string{"", "", "", "", "", "", "yes"}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.conf")
			first, err := askServerOptions(newPrompter(io.Discard, answers(tc.first...)), path, fakeTailscaled(t, true, startable))
			if err != nil {
				t.Fatal(err)
			}
			if err = serversetup.WriteConfig(path, first); err != nil {
				t.Fatal(err)
			}
			rerun, err := askServerOptions(newPrompter(io.Discard, answers(tc.rerun...)), path, fakeTailscaled(t, true, startable))
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(rerun, first) {
				t.Errorf("re-run wrote %v, want the first run's %v", rerun, first)
			}
		})
	}
}

// Changing the answer on a re-run gets what the new answer promises: a no
// turns the edge on even where a yes left it off, and a yes after a no
// turns tailnet sign-in and the dashboard on once the key requirement is
// answered false.
func TestAskServerOptionsRerunChangesTheTailnetAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		first, rerun []string
		want         map[string]string
	}{
		"yes, then no": {[]string{"", "", "yes", "", "", "", "", "yes"}, []string{"", "", "no", "2", "yes"}, map[string]string{
			"tailnet-auto-join": "false", "tailnet-require-key": "true",
			"edge-url": edgeagent.DefaultURL, "edge-access": string(edgeproto.PolicyApprovedDevices),
		}},
		"no, then yes": {[]string{"", "", "no", "2", "yes"}, []string{"", "", "yes", "", "false", "", "", "2", "yes"}, map[string]string{
			"tailnet-auto-join": "false", "tailnet-require-key": "false", "web-port": "443",
			"edge-url": edgeagent.DefaultURL, "edge-access": string(edgeproto.PolicyApprovedDevices),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.conf")
			first, err := askServerOptions(newPrompter(io.Discard, answers(tc.first...)), path, fakeTailscaled(t, true, startable))
			if err != nil {
				t.Fatal(err)
			}
			if err = serversetup.WriteConfig(path, first); err != nil {
				t.Fatal(err)
			}
			rerun, err := askServerOptions(newPrompter(io.Discard, answers(tc.rerun...)), path, fakeTailscaled(t, true, startable))
			if err != nil {
				t.Fatal(err)
			}
			tc.want["addr"], tc.want["data-dir"] = ":2222", "/var/lib/aether"
			if !maps.Equal(rerun, tc.want) {
				t.Errorf("re-run wrote %v, want %v", rerun, tc.want)
			}
		})
	}
}

// A keyed tailnet server with the edge on looks like what a no writes, so a
// yes must offer its key requirement back rather than drop it on Enter.
func TestAskServerOptionsYesKeepsASavedKeyRequirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	saved := map[string]string{"tailnet-auto-join": "false", "tailnet-require-key": "true",
		"edge-url": edgeagent.DefaultURL, "edge-access": string(edgeproto.PolicyApprovedDevices)}
	if err := serversetup.WriteConfig(path, saved); err != nil {
		t.Fatal(err)
	}
	values, err := askServerOptions(newPrompter(io.Discard, answers("", "", "yes", "", "", "", "2", "yes")), path, fakeTailscaled(t, true, startable))
	if err != nil {
		t.Fatal(err)
	}
	if values["tailnet-require-key"] != "true" {
		t.Errorf("tailnet-require-key = %q after a yes with Enter, want the saved true", values["tailnet-require-key"])
	}
	if _, ok := values["web-port"]; ok {
		t.Errorf("web-port = %q, want no dashboard while a key is required", values["web-port"])
	}
}

func TestAskServerOptionsOffersTheDashboardOnATailnet(t *testing.T) {
	if ln, err := net.Listen("tcp", net.JoinHostPort(startable, strconv.Itoa(dashboardPort))); errors.Is(err, syscall.EADDRINUSE) {
		t.Skipf("the dashboard cannot start at %s on this host: %v", startable, err)
	} else if err == nil {
		_ = ln.Close()
	}
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "yes", "", "", "", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
	if err != nil {
		t.Fatal(err)
	}
	if values["web-port"] != "443" {
		t.Errorf("web-port = %q, want 443 by default where the dashboard can start", values["web-port"])
	}
	if !strings.Contains(out.String(), "HTTPS certificates") || strings.Contains(out.String(), "Defaulting to 0") {
		t.Errorf("dashboard prompt does not name what the tailnet needs, or reports a blocker:\n%s", out.String())
	}

	out.Reset()
	values, err = askServerOptions(newPrompter(&out, answers("", "", "yes", "", "", "8443", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
	if err != nil {
		t.Fatal(err)
	}
	if values["web-port"] != "8443" {
		t.Errorf("web-port = %q, want the typed 8443", values["web-port"])
	}
}

func TestAskServerOptionsDefaultsTheDashboardOffWhereItCannotStart(t *testing.T) {
	for name, tc := range map[string]struct {
		tailscaled func(*testing.T) *reachability.Tailscale
		want       string
	}{
		"no HTTPS certificates": {
			func(t *testing.T) *reachability.Tailscale { return fakeTailscaled(t, false, startable) },
			"Defaulting to 0: HTTPS certificates are off for this tailnet; enable MagicDNS and HTTPS certificates on the DNS page of the Tailscale admin console, then run: sudo aether-server config set web-port 443\n",
		},
		"tailscaled unreachable": {
			func(t *testing.T) *reachability.Tailscale {
				return reachability.NewTailscale(filepath.Join(t.TempDir(), "absent.sock"))
			},
			"connect: no such file or directory; connect this host to the tailnet, then run: sudo aether-server config set web-port 443\n",
		},
		"no tailnet address": {
			func(t *testing.T) *reachability.Tailscale { return fakeTailscaled(t, true) },
			"Defaulting to 0: tailscaled reports no tailnet address for box.tail1234.ts.net; connect this host to the tailnet, then run: sudo aether-server config set web-port 443\n",
		},
		"tailnet address not on this host": {
			func(t *testing.T) *reachability.Tailscale { return fakeTailscaled(t, true, "192.0.2.1") },
			"Defaulting to 0: listen tcp 192.0.2.1:443: bind: cannot assign requested address; tailscaled must put the tailnet addresses on this host's TUN interface, which --tun=userspace-networking does not, then run: sudo aether-server config set web-port 443\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			values, err := askServerOptions(newPrompter(&out, answers("", "", "yes", "", "", "", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), tc.tailscaled(t))
			if err != nil {
				t.Fatal(err)
			}
			if values["web-port"] != "0" || !strings.Contains(out.String(), tc.want) {
				t.Errorf("web-port = %q, want 0 and the reason %q in:\n%s", values["web-port"], tc.want, out.String())
			}
			if !strings.Contains(out.String(), "Dashboard HTTPS port (0 = off) [0]: ") {
				t.Errorf("the dashboard question is not asked with 0 as the default:\n%s", out.String())
			}

			values, err = askServerOptions(newPrompter(io.Discard, answers("", "", "yes", "", "", "443", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), tc.tailscaled(t))
			if err != nil {
				t.Fatal(err)
			}
			if values["web-port"] != "443" {
				t.Errorf("web-port = %q, want the typed 443", values["web-port"])
			}
		})
	}
}

func TestAskServerOptionsKeepsAConfiguredDashboardPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"web-port": "443"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "", "", "", "yes")), path, fakeTailscaled(t, false, startable))
	if err != nil {
		t.Fatal(err)
	}
	if values["web-port"] != "443" || strings.Contains(out.String(), "Defaulting to 0") {
		t.Errorf("web-port = %q, want the configured 443 kept without a blocker:\n%s", values["web-port"], out.String())
	}
}

func TestDashboardBlockedByAPortInUse(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	port := held.Addr().(*net.TCPAddr).Port
	err = dashboardBlocked(fakeTailscaled(t, true, "127.0.0.1"), port)
	want := fmt.Sprintf("listen tcp 127.0.0.1:%d: bind: address already in use; answer another port such as 8443 below, or free port %d there", port, port)
	if err == nil || err.Error() != want {
		t.Errorf("dashboardBlocked = %v, want %q", err, want)
	}

	_ = held.Close()
	if err := dashboardBlocked(fakeTailscaled(t, true, "127.0.0.1", "192.0.2.1"), port); err != nil {
		t.Errorf("dashboardBlocked with the port free on one of two addresses = %v, want nil", err)
	}
}

func TestAskServerOptionsSkipsTheDashboardWithoutTailnetIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		tailnet bool
		input   []string
	}{
		"no tailscaled":       {false, []string{"", "", "2", "yes"}},
		"tailnet-require-key": {true, []string{"", "", "yes", "", "true", "", "yes"}},
		"key typed as 1":      {true, []string{"", "", "yes", "", "1", "", "yes"}},
	} {
		t.Run(name, func(t *testing.T) {
			var tailscaled *reachability.Tailscale
			if tc.tailnet {
				tailscaled = fakeTailscaled(t, true, startable)
			}
			var out bytes.Buffer
			values, err := askServerOptions(newPrompter(&out, answers(tc.input...)), filepath.Join(t.TempDir(), "absent.conf"), tailscaled)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := values["web-port"]; ok {
				t.Errorf("web-port = %q, want it left unset", values["web-port"])
			}
			if strings.Contains(out.String(), "Dashboard HTTPS port") {
				t.Errorf("dashboard prompt shown where it cannot start:\n%s", out.String())
			}
		})
	}
}

func TestAskServerOptionsTurnsTheEdgeOnWithoutTailscale(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "2", "yes")), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["edge-url"] != edgeagent.DefaultURL {
		t.Errorf("edge-url = %q, want %s without Tailscale", values["edge-url"], edgeagent.DefaultURL)
	}
	for _, want := range []string{"Tailscale is not installed", "run by the Aether project", "host name, host key and IP address",
		"client IP addresses", `aether-server config set edge-url ""`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("setup does not say %q:\n%s", want, out.String())
		}
	}
}

func TestAskServerOptionsKeepsTheEdgeOffWhenConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"edge-url": ""}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "yes")), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := values["edge-url"]; !ok || v != "" {
		t.Errorf("edge-url = %q, %v; want it kept off", v, ok)
	}
}

func TestAskServerOptionsOffersTheEdgeOnATailnet(t *testing.T) {
	for answer, want := range map[string]string{"": "", "yes": edgeagent.DefaultURL} {
		input := []string{"", "", "yes", "", "", "", answer}
		if want != "" {
			input = append(input, "1")
		}
		var out bytes.Buffer
		values, err := askServerOptions(newPrompter(&out, answers(append(input, "yes")...)), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
		if err != nil {
			t.Fatal(err)
		}
		if values["edge-url"] != want {
			t.Errorf("answer %q: edge-url = %q, want %q", answer, values["edge-url"], want)
		}
	}
}

// The policy question has no default: Enter and anything but 1 or 2 ask
// again, and input that ends unanswered writes nothing.
func TestAskServerOptionsRequiresAnAccessPolicy(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "3", "account", "2", "yes")), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["edge-access"] != string(edgeproto.PolicyApprovedDevices) {
		t.Errorf("edge-access = %q, want approved-devices from the answer 2", values["edge-access"])
	}
	if n := strings.Count(out.String(), "Choose 1 or 2:"); n != 4 {
		t.Errorf("asked %d times for three unusable answers and a 2, want 4:\n%s", n, out.String())
	}
	for _, want := range []string{"Who may reach this server through the edge?", "1) Account access", "2) Approved devices",
		"a taken-over GitHub account or edge cannot add one",
		"Tailscale and SSH key access stay as they are.",
		"aether-server config set edge-access <account|approved-devices>"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("setup does not say %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	values, err = askServerOptions(newPrompter(&out, answers("", "", "", "", "")), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err == nil || values != nil {
		t.Fatalf("setup without a policy answer = %v, %v; want refused", values, err)
	}
}

// Without Tailscale and with the edge left off, no policy is asked or
// written.
func TestAskServerOptionsAsksNoPolicyWithTheEdgeOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"edge-url": ""}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "yes")), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values["edge-access"]; ok || strings.Contains(out.String(), "Choose 1 or 2") {
		t.Errorf("policy asked or written with the edge off: %v\n%s", values, out.String())
	}
}

func fakeSysbox(t *testing.T, on bool, install func(io.Writer) error) *int {
	t.Helper()
	oldStatus, oldInstall := sysboxStatus, sysboxInstall
	t.Cleanup(func() { sysboxStatus, sysboxInstall = oldStatus, oldInstall })
	sysboxStatus = func(context.Context) (string, error) {
		if on {
			return protocol.SysboxOn, nil
		}
		return protocol.SysboxOff, nil
	}
	installs := new(int)
	sysboxInstall = func(_ context.Context, w io.Writer) error {
		*installs++
		return install(w)
	}
	return installs
}

func TestAskSysboxDefaultsToYes(t *testing.T) {
	fakeSysbox(t, false, nil)
	for answer, want := range map[string]bool{"": true, "yes": true, "n": false, "no": false} {
		var out bytes.Buffer
		if got := askSysbox(newPrompter(&out, answers(answer)), true); got != want {
			t.Errorf("answer %q = %v, want %v", answer, got, want)
		}
		if !strings.Contains(out.String(), "Let agents use Docker inside their sessions? This installs the Sysbox runtime (Ubuntu/Debian)") {
			t.Errorf("prompt = %q", out.String())
		}
	}
}

func TestAskSysboxIsSilentWhenSysboxIsOn(t *testing.T) {
	fakeSysbox(t, true, nil)
	var out bytes.Buffer
	if askSysbox(newPrompter(&out, answers("yes")), true) || out.Len() != 0 {
		t.Errorf("askSysbox with Sysbox on = prompt %q, want no question", out.String())
	}
}

func TestAskSysboxIsSkippedWithoutDocker(t *testing.T) {
	fakeSysbox(t, false, nil)
	var out bytes.Buffer
	if askSysbox(newPrompter(&out, answers("yes")), false) || out.String() != "\nSkipping Docker inside sessions (Sysbox), which needs Docker; once Docker answers: sudo aether-server sysbox install\n" {
		t.Errorf("askSysbox without Docker = output %q, want the skip line and no question", out.String())
	}
}

// Enter, like no, stops setup before the first option is asked.
func TestAskWithoutDockerDefaultsToStopping(t *testing.T) {
	for answer, stop := range map[string]bool{"": true, "no": true, "yes": false} {
		var out bytes.Buffer
		err := askWithoutDocker(newPrompter(&out, answers(answer)))
		stopped := err != nil && err.Error() == "nothing written; install or start Docker, then run setup again"
		if stopped != stop || (!stop && err != nil) || out.String() != "Continue without Docker (yes/no) [no]: " {
			t.Errorf("answer %q = %v, prompt %q; want stop %v", answer, err, out.String(), stop)
		}
	}
}

// A server set up without Docker cannot start, so setup issues no claim
// code to expire meanwhile, and next: starts on Docker.
func TestWriteSetupWithoutDockerStartsNextOnDocker(t *testing.T) {
	t.Setenv("SUDO_USER", "ubuntu")
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	values := map[string]string{"data-dir": dataDir, "edge-url": edgeagent.DefaultURL, "edge-access": string(edgeproto.PolicyApprovedDevices)}
	configPath := filepath.Join(dir, "server.conf")
	var out bytes.Buffer
	if err := writeSetup(&out, filepath.Join(dir, "aether-server.service"), configPath, values, false, false, false, ""); err != nil {
		t.Fatal(err)
	}
	want := "  1. install or start Docker on this machine:\n       https://docs.docker.com/engine/install/\n" +
		"  2. start the server on this machine:\n       sudo systemctl daemon-reload && sudo systemctl enable --now aether-server\n" +
		"  3. on this machine, print a claim code:\n       sudo aether-server edge claim-code --config " + configPath + "\n" +
		installStep(4) +
		"  5. on your computer, sign in and claim the server with that code:\n       aether login\n       aether link --claim <code>\n" +
		mcpStep(6)
	if got := nextSteps(t, out.String()); got != want {
		t.Errorf("next steps:\n%s\nwant:\n%s", got, want)
	}
	if _, ok, err := edgeagent.OpenState(dataDir).ClaimCode(); err != nil || ok || strings.Contains(out.String(), "claim link: ") {
		t.Errorf("claim code issued = %v (%v), want none:\n%s", ok, err, out.String())
	}
}

func TestInstallSysboxReportsWithoutFailingSetup(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"done":        {nil, ""},
		"unsupported": {&sysbox.UnsupportedError{Reason: "kernel 5.4 cannot ID-map bind mounts"}, "Docker inside sessions is unavailable on this host: kernel 5.4 cannot ID-map bind mounts\n"},
		"failure":     {errors.New("sysbox: install: exit status 1: checksum mismatch"), "checksum mismatch\nInstall it later with: sudo aether-server sysbox install\n"},
		"detached": {&sysbox.DetachedError{Unit: "aether-sysbox-install"}, "Stopped waiting for Sysbox: the installer keeps running as the aether-sysbox-install unit; " +
			"`systemctl status aether-sysbox-install` shows whether it has finished\nOnce it has, check Docker inside sessions with: aether-server sysbox status\n"},
	} {
		t.Run(name, func(t *testing.T) {
			installs := fakeSysbox(t, false, func(io.Writer) error { return tc.err })
			var out bytes.Buffer
			installSysbox(&out)
			if *installs != 1 || !strings.Contains(out.String(), tc.want) {
				t.Errorf("installs = %d, output %q; want it to contain %q", *installs, out.String(), tc.want)
			}
		})
	}
}

// The next steps come last: below the Sysbox installer's output and the
// claim link, with the activation line once and no escape codes off a
// terminal.
func TestWriteSetupEndsOnNextSteps(t *testing.T) {
	fakeSysbox(t, false, func(w io.Writer) error {
		_, err := io.WriteString(w, "sysbox installer output\n")
		return err
	})
	t.Setenv("SUDO_USER", "")
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	values := map[string]string{"data-dir": dataDir, "edge-url": edgeagent.DefaultURL, "edge-access": string(edgeproto.PolicyApprovedDevices)}
	var out bytes.Buffer
	if err := writeSetup(&out, filepath.Join(dir, "aether-server.service"), filepath.Join(dir, "server.conf"), values, false, true, true, ""); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	code := strings.TrimPrefix(lines[len(lines)-3], "       aether link --claim ")
	if claim, ok, err := edgeagent.OpenState(dataDir).ClaimCode(); err != nil || !ok || claim.Hash != edgeproto.HashToken(code) {
		t.Fatalf("line %q does not carry the issued claim code (%v, %v):\n%s", lines[len(lines)-3], ok, err, got)
	}
	sysboxAt, claimAt, nextAt := strings.Index(got, "sysbox installer output"), strings.Index(got, "claim link: "), strings.Index(got, "\nnext:\n")
	if sysboxAt < 0 || claimAt < sysboxAt || nextAt < claimAt {
		t.Errorf("want Sysbox output, then the claim link, then the next steps:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n       "+serversetup.ActivateCommand+"\n"+installStep(2)+
		"  3. on your computer: in the Aether app's Connect step choose Aether relay, sign in\n"+
		"     with GitHub and paste the claim link above; in a terminal:\n"+
		"       aether login\n"+
		"       aether link --claim "+code+"\n"+mcpStep(4)) {
		t.Errorf("output does not end on starting the server, claiming it and adding it to ChatGPT or Claude:\n%s", got)
	}
	if n := strings.Count(got, "systemctl daemon-reload"); n != 1 {
		t.Errorf("activation line printed %d times, want once:\n%s", n, got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape codes written to a non-terminal:\n%q", got)
	}
}

// installStep is the next step, numbered n, that installs the client.
func installStep(n int) string {
	return fmt.Sprintf("  %d. on your computer, install Aether if it is not there yet (Windows: docs/install.md):\n       curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh -s -- --client\n", n)
}

// mcpStep is the next step, numbered n, that a server with the edge on
// ends with.
func mcpStep(n int) string {
	return fmt.Sprintf("  %d. optional, to run agents from ChatGPT or Claude, on that computer:\n       aether mcp\n", n)
}

// optionsOf is the serve options with values set, as configuredOptions
// returns them.
func optionsOf(t *testing.T, values map[string]string) *flag.FlagSet {
	t.Helper()
	fs := serveFlagSet()
	if _, err := serversetup.Apply(fs, values); err != nil {
		t.Fatal(err)
	}
	return fs
}

// On a tailnet the next steps name this host's MagicDNS name, with the
// port only when aether link would not assume it, and the dashboard's
// address when the server serves it.
func TestPrintNextStepsNamesTheTailnetHost(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	for _, tc := range []struct{ addr, web, link, open string }{
		{":2222", "0", "box.tail1234.ts.net", ""},
		{":2300", "0", "box.tail1234.ts.net:2300", ""},
		{":2222", "443", "box.tail1234.ts.net", "     or, with nothing installed, open https://box.tail1234.ts.net/ in a browser there\n"},
		{":2222", "8443", "box.tail1234.ts.net", "     or, with nothing installed, open https://box.tail1234.ts.net:8443/ in a browser there\n"},
	} {
		var out bytes.Buffer
		o := optionsOf(t, map[string]string{"addr": tc.addr, "web-port": tc.web, "edge-url": ""})
		if err := printNextSteps(&out, serversetup.DefaultConfigPath, o, "", true, "box.tail1234.ts.net"); err != nil {
			t.Fatal(err)
		}
		want := installStep(2) +
			"  3. on your computer, on the same tailnet: in the Aether app's Connect step choose\n" +
			"     Tailscale and enter " + tc.link + "; in a terminal:\n" +
			"       aether link " + tc.link + "\n" + tc.open
		if !strings.HasSuffix(out.String(), want) || strings.Contains(out.String(), "<this-host>") {
			t.Errorf("addr %s, web-port %s: next steps do not end on linking %s:\n%s", tc.addr, tc.web, tc.link, out.String())
		}
	}
}

// A saved key requirement means a tailnet login alone does not link, and
// SSH bound to one address answers only there, so neither gets the
// tailnet line; with the edge on too, the claim is the alternative to the
// tailnet link, not a step after it.
func TestPrintNextStepsFollowsTheSavedTailnetConfig(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	var out bytes.Buffer
	o := optionsOf(t, map[string]string{"edge-url": "", "tailnet-require-key": "true"})
	if err := printNextSteps(&out, serversetup.DefaultConfigPath, o, "", true, "box.tail1234.ts.net"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "box.tail1234.ts.net") || !strings.Contains(out.String(), "aether link <this-host>:2222") {
		t.Errorf("a key requirement still gets the tailnet line:\n%s", out.String())
	}

	out.Reset()
	o = optionsOf(t, map[string]string{"addr": "192.168.1.10:2222", "edge-url": ""})
	if err := printNextSteps(&out, serversetup.DefaultConfigPath, o, "", true, "box.tail1234.ts.net"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "box.tail1234.ts.net") || !strings.Contains(out.String(), "aether link 192.168.1.10:2222") {
		t.Errorf("SSH bound to one address does not send the link there:\n%s", out.String())
	}

	out.Reset()
	o = optionsOf(t, map[string]string{"data-dir": t.TempDir(), "edge-url": edgeagent.DefaultURL})
	if err := printNextSteps(&out, serversetup.DefaultConfigPath, o, "code", true, "box.tail1234.ts.net"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "aether link box.tail1234.ts.net\n  4. or, on a computer off the tailnet: in the Aether app's Connect step choose Aether relay") {
		t.Errorf("the claim does not read as the alternative to the tailnet link:\n%s", out.String())
	}
}

// Setup run through sudo gets a command its user can paste; a root shell
// gets the bare one.
func TestPrintNextStepsPrefixesSudoForASudoUser(t *testing.T) {
	for sudoUser, want := range map[string]string{
		"ubuntu": "sudo systemctl daemon-reload && sudo systemctl enable --now aether-server",
		"":       "\n       " + serversetup.ActivateCommand + "\n",
	} {
		t.Setenv("SUDO_USER", sudoUser)
		var out bytes.Buffer
		if err := printNextSteps(&out, serversetup.DefaultConfigPath, optionsOf(t, map[string]string{"addr": ":2300"}), "", true, ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), want) || !strings.HasSuffix(out.String(), "\n       aether link <this-host>:2300\n") {
			t.Errorf("SUDO_USER=%q: want %q and a link to the configured port:\n%s", sudoUser, want, out.String())
		}
	}
}

// `aether-server install --edge-url` issues no claim code, so on a server
// without an owner step 2 is the command that issues one, and step 3
// signs in before claiming, on the server's edge: a claim code names none.
func TestPrintNextStepsSendsAnUnclaimedEdgeServerToClaimCode(t *testing.T) {
	t.Setenv("SUDO_USER", "ubuntu")
	claim := installStep(3) + "  4. on your computer, sign in and claim the server with that code:\n       aether login\n       aether link --claim <code>\n"
	var out bytes.Buffer
	o := optionsOf(t, map[string]string{"data-dir": t.TempDir(), "edge-url": edgeagent.DefaultURL})
	if err := printNextSteps(&out, serversetup.DefaultConfigPath, o, "", true, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "\n       sudo aether-server edge claim-code\n"+claim+mcpStep(5)) {
		t.Errorf("want step 2 to issue a claim code and step 3 to use it:\n%s", out.String())
	}

	out.Reset()
	if err := printNextSteps(&out, "/srv/it's here/server.conf", o, "", true, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), `sudo aether-server edge claim-code --config '/srv/it'\''s here/server.conf'`+"\n"+claim+mcpStep(5)) {
		t.Errorf("want the claim command to name the config install wrote, shell-quoted:\n%s", out.String())
	}

	out.Reset()
	o = optionsOf(t, map[string]string{"data-dir": t.TempDir(), "edge-url": "https://edge.example"})
	if err := printNextSteps(&out, serversetup.DefaultConfigPath, o, "", true, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n       aether login --edge https://edge.example\n       aether link --claim <code> --edge https://edge.example\n") {
		t.Errorf("want both client commands on the server's edge:\n%s", out.String())
	}
}

// An addr without a port fails before the block starts, never halfway
// through it.
func TestPrintNextStepsRefusesAnAddrWithoutAPortBeforePrinting(t *testing.T) {
	var out bytes.Buffer
	err := printNextSteps(&out, serversetup.DefaultConfigPath, optionsOf(t, map[string]string{"addr": "2222"}), "", true, "")
	if err == nil || !strings.Contains(err.Error(), `addr "2222"`) || out.Len() != 0 {
		t.Errorf("err = %v, output %q; want the addr error and nothing printed", err, out.String())
	}
}
