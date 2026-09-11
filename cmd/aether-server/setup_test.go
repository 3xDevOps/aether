package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/serversetup"
)

// answers builds the stdin an operator would type: one line per prompt, in
// the order askServerOptions asks them, ending with the write confirmation.
func answers(lines ...string) *strings.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestAskServerOptionsEmptyAnswersTakeDefaults(t *testing.T) {
	var out bytes.Buffer
	in := answers("", "", "", "", "yes")
	values, err := askServerOptions(&out, in, filepath.Join(t.TempDir(), "absent.conf"), false)
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
	in := answers(":2300", "/srv/aether", "true", "true", "yes")
	values, err := askServerOptions(&out, in, filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err := askServerOptions(&out, answers("", "", "", "", "yes"), path, false)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2300" || values["data-dir"] != "/srv/aether" {
		t.Errorf("values = %v, want the existing config as the defaults", values)
	}
}

func TestAskServerOptionsRejectsBadValueAndReasks(t *testing.T) {
	var out bytes.Buffer
	in := answers("", "", "not-a-bool", "true", "", "yes")
	values, err := askServerOptions(&out, in, filepath.Join(t.TempDir(), "absent.conf"), false)
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
	values, err := askServerOptions(&out, answers("", "", "", "", "no"), filepath.Join(t.TempDir(), "absent.conf"), false)
	if err != nil {
		t.Fatal(err)
	}
	if values != nil {
		t.Errorf("values = %v, want nil when the operator declines", values)
	}
}

func TestAskServerOptionsOffersTheDashboardOnATailnet(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(&out, answers("", "", "", "", "", "yes"), filepath.Join(t.TempDir(), "absent.conf"), true)
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
	values, err = askServerOptions(&out, answers("", "", "", "", "8443", "yes"), filepath.Join(t.TempDir(), "absent.conf"), true)
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
		"no tailscaled":       {false, []string{"", "", "", "", "yes"}},
		"tailnet-require-key": {true, []string{"", "", "", "true", "yes"}},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			values, err := askServerOptions(&out, answers(tc.input...), filepath.Join(t.TempDir(), "absent.conf"), tc.tailnet)
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
