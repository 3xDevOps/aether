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
	in := answers("", "", "", "", "", "", "yes")
	values, err := askServerOptions(&out, in, filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"addr":                ":2222",
		"dashboard-port":      "8080",
		"data-dir":            "/var/lib/aether",
		"tailnet-auto-join":   "false",
		"tailnet-require-key": "false",
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q, want the default %q", k, values[k], v)
		}
	}
	if _, ok := values["dashboard-addr"]; ok {
		t.Error("an empty dashboard address must stay out of the file, not be written as empty")
	}
	if !strings.Contains(out.String(), "anyone already on your tailnet") {
		t.Errorf("auto-join prompt does not explain the security tradeoff:\n%s", out.String())
	}
}

func TestAskServerOptionsUsesTypedAnswers(t *testing.T) {
	var out bytes.Buffer
	in := answers(":2300", "9090", "/srv/aether", "100.64.0.1:8080", "true", "true", "yes")
	values, err := askServerOptions(&out, in, filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"addr":                ":2300",
		"dashboard-port":      "9090",
		"data-dir":            "/srv/aether",
		"dashboard-addr":      "100.64.0.1:8080",
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
	values, err := askServerOptions(&out, answers("", "", "", "", "", "", "yes"), path)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2300" || values["data-dir"] != "/srv/aether" {
		t.Errorf("values = %v, want the existing config as the defaults", values)
	}
}

func TestAskServerOptionsRejectsBadValueAndReasks(t *testing.T) {
	var out bytes.Buffer
	in := answers("", "not-a-port", "9090", "", "", "", "", "yes")
	values, err := askServerOptions(&out, in, filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if values["dashboard-port"] != "9090" {
		t.Errorf("dashboard-port = %q, want the retyped 9090", values["dashboard-port"])
	}
	if !strings.Contains(out.String(), "try again") {
		t.Errorf("a rejected answer must be re-asked:\n%s", out.String())
	}
}

func TestAskServerOptionsDeclinedWritesNothing(t *testing.T) {
	var out bytes.Buffer
	values, err := askServerOptions(&out, answers("", "", "", "", "", "", "no"), filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if values != nil {
		t.Errorf("values = %v, want nil when the operator declines", values)
	}
}
