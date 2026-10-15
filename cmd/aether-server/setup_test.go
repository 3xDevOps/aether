package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
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
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "2", "yes")), path, false)
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
	values, err := askServerOptions(newPrompter(&out, in), filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "2", "no")), filepath.Join(t.TempDir(), "absent.conf"), false)
	if err != nil {
		t.Fatal(err)
	}
	if values != nil {
		t.Errorf("values = %v, want nil when the operator declines", values)
	}
}

func TestAskServerOptionsOffersTheDashboardOnATailnet(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), true)
	if err != nil {
		t.Fatal(err)
	}
	if values["web-port"] != "443" {
		t.Errorf("web-port = %q, want 443 by default on a tailnet host", values["web-port"])
	}
	if !strings.Contains(out.String(), "HTTPS certificates") {
		t.Errorf("dashboard prompt does not name what the tailnet needs:\n%s", out.String())
	}

	out.Reset()
	values, err = askServerOptions(newPrompter(&out, answers("", "", "", "", "8443", "", "yes")), filepath.Join(t.TempDir(), "absent.conf"), true)
	if err != nil {
		t.Fatal(err)
	}
	if values["web-port"] != "8443" {
		t.Errorf("web-port = %q, want the typed 8443", values["web-port"])
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
			var out bytes.Buffer
			values, err := askServerOptions(newPrompter(&out, answers(tc.input...)), filepath.Join(t.TempDir(), "absent.conf"), tc.tailnet)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "2", "yes")), filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "yes")), path, false)
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
		values, err := askServerOptions(newPrompter(&out, answers(append(input, "yes")...)), filepath.Join(t.TempDir(), "absent.conf"), true)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "", "3", "account", "2", "yes")), filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err = askServerOptions(newPrompter(&out, answers("", "", "", "", "")), filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err := askServerOptions(newPrompter(&out, answers("", "", "", "", "yes")), path, false)
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
