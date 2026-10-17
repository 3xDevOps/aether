package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
// Without Tailscale the edge is on, and the last answer before the
// confirmation chooses its access policy.
func answers(lines ...string) *strings.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestAskServerOptionsEmptyAnswersTakeDefaults(t *testing.T) {
	var out bytes.Buffer
	in := answers("", "", "", "", "2", "yes")
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"addr":                ":2222",
		"data-dir":            "/var/lib/aether",
		"tailnet-auto-join":   "false",
		"tailnet-require-key": "false",
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q, want the default %q", k, values[k], v)
		}
	}
	if !strings.Contains(out.String(), "anyone already on your tailnet") {
		t.Errorf("auto-join prompt does not explain the security tradeoff:\n%s", out.String())
	}
}

func TestAskServerOptionsUsesTypedAnswers(t *testing.T) {
	var out bytes.Buffer
	in := answers(":2300", "/srv/aether", "true", "true", "1", "yes")
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), nil)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "2", "yes")), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2300" || values["data-dir"] != "/srv/aether" {
		t.Errorf("values = %v, want the existing config as the defaults", values)
	}
}

func TestAskServerOptionsRejectsBadValueAndReasks(t *testing.T) {
	var out bytes.Buffer
	in := answers("", "", "not-a-bool", "true", "", "2", "yes")
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["tailnet-auto-join"] != "true" {
		t.Errorf("tailnet-auto-join = %q, want the retyped true", values["tailnet-auto-join"])
	}
	if !strings.Contains(out.String(), "try again") {
		t.Errorf("a rejected answer must be re-asked:\n%s", out.String())
	}
}

func TestAskServerOptionsDeclinedWritesNothing(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "2", "no")), filepath.Join(t.TempDir(), "absent.conf"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if values != nil {
		t.Errorf("values = %v, want nil when the operator declines", values)
	}
}

// fakeTailscaled answers the LocalAPI status request the way tailscaled
// does for a node at addrs, reporting a certificate name when certs is set,
// as a tailnet with HTTPS certificates enabled does.
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
		_, _ = fmt.Fprintf(w, `{"Self":{"DNSName":"box.tail1234.ts.net.","TailscaleIPs":%s},"CertDomains":%s}`, ips, certDomains)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return reachability.NewTailscale(sock)
}

// startable is a tailnet address that binds port 443 as root and refuses
// it only with EACCES otherwise, so the dashboard can start there either way.
const startable = "127.0.0.1"

func TestAskServerOptionsOffersTheDashboardOnATailnet(t *testing.T) {
	if ln, err := net.Listen("tcp", net.JoinHostPort(startable, strconv.Itoa(dashboardPort))); errors.Is(err, syscall.EADDRINUSE) {
		t.Skipf("the dashboard cannot start at %s on this host: %v", startable, err)
	} else if err == nil {
		_ = ln.Close()
	}
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
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
	values, err = askServerOptions(newPrompter(&out, answers("", "", "", "", "8443", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), fakeTailscaled(t, true, startable))
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
			values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), tc.tailscaled(t))
			if err != nil {
				t.Fatal(err)
			}
			if values["web-port"] != "0" || !strings.Contains(out.String(), tc.want) {
				t.Errorf("web-port = %q, want 0 and the reason %q in:\n%s", values["web-port"], tc.want, out.String())
			}
			if !strings.Contains(out.String(), "Dashboard HTTPS port on the tailnet (0 = off) [0]: ") {
				t.Errorf("the dashboard question is not asked with 0 as the default:\n%s", out.String())
			}

			values, err = askServerOptions(newPrompter(io.Discard, answers("", "", "", "", "443", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), tc.tailscaled(t))
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "", "", "yes")), path, fakeTailscaled(t, false, startable))
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
		"no tailscaled":       {false, []string{"", "", "", "", "2", "yes"}},
		"tailnet-require-key": {true, []string{"", "", "", "true", "", "yes"}},
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "2", "yes")), filepath.Join(t.TempDir(), "absent.conf"), nil)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "yes")), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := values["edge-url"]; !ok || v != "" {
		t.Errorf("edge-url = %q, %v; want it kept off", v, ok)
	}
}

func TestAskServerOptionsOffersTheEdgeOnATailnet(t *testing.T) {
	for answer, want := range map[string]string{"": "", "yes": edgeagent.DefaultURL} {
		input := []string{"", "", "", "", "", answer}
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "", "3", "account", "2", "yes")), filepath.Join(t.TempDir(), "absent.conf"), nil)
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
		"A taken-over\n     account, or a compromised edge, cannot add a device on its own.",
		"Both leave Tailscale and direct SSH key access as they are.",
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "yes")), path, nil)
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
		if got := askSysbox(newPrompter(&out, answers(answer))); got != want {
			t.Errorf("answer %q = %v, want %v", answer, got, want)
		}
		if !strings.Contains(out.String(), "Let agents use Docker inside their runs? This installs the Sysbox runtime (Ubuntu/Debian)") {
			t.Errorf("prompt = %q", out.String())
		}
	}
}

func TestAskSysboxIsSilentWhenSysboxIsOn(t *testing.T) {
	fakeSysbox(t, true, nil)
	var out bytes.Buffer
	if askSysbox(newPrompter(&out, answers("yes"))) || out.Len() != 0 {
		t.Errorf("askSysbox with Sysbox on = prompt %q, want no question", out.String())
	}
}

func TestInstallSysboxReportsWithoutFailingSetup(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"done":        {nil, ""},
		"unsupported": {&sysbox.UnsupportedError{Reason: "kernel 5.4 cannot ID-map bind mounts"}, "Docker inside runs is unavailable on this host: kernel 5.4 cannot ID-map bind mounts\n"},
		"failure":     {errors.New("sysbox: install: exit status 1: checksum mismatch"), "checksum mismatch\nInstall it later with: sudo aether-server sysbox install\n"},
		"detached": {&sysbox.DetachedError{Unit: "aether-sysbox-install"}, "Stopped waiting for Sysbox: the installer keeps running as the aether-sysbox-install unit; " +
			"`systemctl status aether-sysbox-install` shows whether it has finished\nOnce it has, check Docker inside runs with: aether-server sysbox status\n"},
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
	if err := writeSetup(&out, filepath.Join(dir, "aether-server.service"), filepath.Join(dir, "server.conf"), values, false, true); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	code := strings.TrimPrefix(lines[len(lines)-1], "       aether link --claim ")
	if claim, ok, err := edgeagent.OpenState(dataDir).ClaimCode(); err != nil || !ok || claim.Hash != edgeproto.HashToken(code) {
		t.Fatalf("last line %q does not carry the issued claim code (%v, %v):\n%s", lines[len(lines)-1], ok, err, got)
	}
	sysboxAt, claimAt, nextAt := strings.Index(got, "sysbox installer output"), strings.Index(got, "claim link: "), strings.Index(got, "\nnext:\n")
	if sysboxAt < 0 || claimAt < sysboxAt || nextAt < claimAt {
		t.Errorf("want Sysbox output, then the claim link, then the next steps:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n       "+serversetup.ActivateCommand+"\n"+
		"  2. on your computer, open the claim link above with the Aether app, or paste it\n"+
		"     into the app's Connect page; in a terminal:\n"+
		"       aether link --claim "+code+"\n") {
		t.Errorf("output does not end on starting the server and claiming it:\n%s", got)
	}
	if n := strings.Count(got, "systemctl daemon-reload"); n != 1 {
		t.Errorf("activation line printed %d times, want once:\n%s", n, got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape codes written to a non-terminal:\n%q", got)
	}
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

// Setup run through sudo gets a command its user can paste; a root shell
// gets the bare one.
func TestPrintNextStepsPrefixesSudoForASudoUser(t *testing.T) {
	for sudoUser, want := range map[string]string{
		"ubuntu": "sudo systemctl daemon-reload && sudo systemctl enable --now aether-server",
		"":       "\n       " + serversetup.ActivateCommand + "\n",
	} {
		t.Setenv("SUDO_USER", sudoUser)
		var out bytes.Buffer
		if err := printNextSteps(&out, serversetup.DefaultConfigPath, optionsOf(t, map[string]string{"addr": ":2300"}), ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), want) || !strings.HasSuffix(out.String(), "\n       aether link <this-host>:2300\n") {
			t.Errorf("SUDO_USER=%q: want %q and a link to the configured port:\n%s", sudoUser, want, out.String())
		}
	}
}

// `aether-server install --edge-url` issues no claim code, so on a server
// without an owner step 2 is the command that issues one.
func TestPrintNextStepsSendsAnUnclaimedEdgeServerToClaimCode(t *testing.T) {
	t.Setenv("SUDO_USER", "ubuntu")
	var out bytes.Buffer
	o := optionsOf(t, map[string]string{"data-dir": t.TempDir(), "edge-url": edgeagent.DefaultURL})
	if err := printNextSteps(&out, serversetup.DefaultConfigPath, o, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "\n       sudo aether-server edge claim-code\n") {
		t.Errorf("want step 2 to issue a claim code:\n%s", out.String())
	}

	out.Reset()
	if err := printNextSteps(&out, "/srv/it's here/server.conf", o, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), `sudo aether-server edge claim-code --config '/srv/it'\''s here/server.conf'`+"\n") {
		t.Errorf("want the claim command to name the config install wrote, shell-quoted:\n%s", out.String())
	}
}

// An addr without a port fails before the block starts, never halfway
// through it.
func TestPrintNextStepsRefusesAnAddrWithoutAPortBeforePrinting(t *testing.T) {
	var out bytes.Buffer
	err := printNextSteps(&out, serversetup.DefaultConfigPath, optionsOf(t, map[string]string{"addr": "2222"}), "")
	if err == nil || !strings.Contains(err.Error(), `addr "2222"`) || out.Len() != 0 {
		t.Errorf("err = %v, output %q; want the addr error and nothing printed", err, out.String())
	}
}
