package localgw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/localops"
)

// writeScanStub writes an executable shell script and returns the argv
// override that runs it with the rendered prompt as its first argument,
// mirroring the localops envscan tests. The stubs are POSIX shell
// scripts, so every test that runs one skips on Windows.
func writeScanStub(t *testing.T, body string) []string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("stub harnesses are POSIX shell scripts")
	}
	script := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", script, harness.TaskPlaceholder}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stubLoginShell replaces the login shell the harness verb and the scan
// handler ask for PATH with a script running body, so no test runs the
// developer's real shell or inherits its PATH; PATH is re-set so the
// widening the handler writes is undone after the test. Windows
// never asks a shell, so nothing is stubbed there.
func stubLoginShell(t *testing.T, body string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		return
	}
	shell := filepath.Join(t.TempDir(), "shell.sh")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", os.Getenv("PATH"))
}

// loginShellAnswer is a stub shell body that answers the PATH probe the
// way a working login shell does, with one folder of its own.
const loginShellAnswer = "printf '__AETHER_PATH_BEGIN__%s__AETHER_PATH_END__\\n' /stub/login\n"

// envHarnessesResult decodes the env.harnesses answer.
type envHarnessesResult struct {
	Harnesses []localops.HarnessStatus `json:"harnesses"`
	Searched  []string                 `json:"searched"`
	Warning   string                   `json:"warning"`
}

// callEnvHarnesses answers env.harnesses on a fresh gateway with a stub
// claude executable in a folder of its own on PATH and an empty HOME, so
// no per-user fallback folder joins the search.
func callEnvHarnesses(t *testing.T) (bin string, got envHarnessesResult) {
	t.Helper()
	bin = t.TempDir()
	t.Setenv("HOME", t.TempDir())
	// Windows PATH lookup only finds files with an executable extension,
	// so the stub carries one there.
	stub := "claude"
	if goruntime.GOOS == "windows" {
		stub += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, stub), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})
	rec := do(g, http.MethodPost, "/local/v1/env.harnesses", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("env.harnesses = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return bin, got
}

// TestLocalEnvHarnesses: the verb widens PATH from the login shell before
// looking, reports the folders it searched with the shell's entries
// first, and finds the stub claude. The other harnesses' state depends on
// the machine's own /usr/local/bin and /opt/homebrew/bin, which the
// widening always adds when they exist, so only the names are checked.
func TestLocalEnvHarnesses(t *testing.T) {
	stubLoginShell(t, loginShellAnswer)
	bin, got := callEnvHarnesses(t)

	wantSearched := []string{"/stub/login", bin}
	if goruntime.GOOS == "windows" {
		wantSearched = []string{bin}
	}
	if len(got.Searched) < len(wantSearched) || fmt.Sprint(got.Searched[:len(wantSearched)]) != fmt.Sprint(wantSearched) {
		t.Errorf("searched = %v, want it to start with %v", got.Searched, wantSearched)
	}
	if got.Warning != "" {
		t.Errorf("warning = %q, want none", got.Warning)
	}
	wantNames := []string{"claude", "codex", "pi", "amp"}
	if len(got.Harnesses) != len(wantNames) {
		t.Fatalf("harnesses = %+v, want %v", got.Harnesses, wantNames)
	}
	for i, name := range wantNames {
		if got.Harnesses[i].Name != name {
			t.Errorf("harness %d = %+v, want %s", i, got.Harnesses[i], name)
		}
	}
	if !got.Harnesses[0].Installed {
		t.Errorf("claude = %+v, want installed from %s", got.Harnesses[0], bin)
	}
}

// TestLocalEnvHarnessesShellWarning: a login shell that fails still
// answers the harness list, checked against the standard folders, and
// names the failed run in warning so the wizard can show it.
func TestLocalEnvHarnessesShellWarning(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows never asks a login shell")
	}
	stubLoginShell(t, "exit 1\n")
	bin, got := callEnvHarnesses(t)

	if !strings.Contains(got.Warning, "read PATH from the login shell") || !strings.Contains(got.Warning, "-l -i -c: exit status 1") {
		t.Errorf("warning = %q, want the failed login shell run named", got.Warning)
	}
	if len(got.Searched) == 0 || got.Searched[0] != bin {
		t.Errorf("searched = %v, want it to start with %s", got.Searched, bin)
	}
	if len(got.Harnesses) == 0 || !got.Harnesses[0].Installed {
		t.Errorf("harnesses = %+v, want claude installed from %s", got.Harnesses, bin)
	}
}

// TestLocalEnvHarnessesRepoSuggestion: the verb suggests the one
// repository folder the saved link config knows, so the wizard can
// prefill the from-repo folder input; several distinct folders or none
// mean no safe guess and the key is omitted.
func TestLocalEnvHarnessesRepoSuggestion(t *testing.T) {
	stubLoginShell(t, loginShellAnswer)
	cases := map[string]struct {
		cfg  cli.Config
		want string
	}{
		"linked repo": {
			cfg:  cli.Config{Repo: "/src/repo"},
			want: "/src/repo",
		},
		"same repo across profiles": {
			cfg: cli.Config{Repo: "/src/repo", Links: []cli.NamedLink{
				{Name: "prod", Repo: "/src/repo"},
				{Name: "staging"},
			}},
			want: "/src/repo",
		},
		"profile-only repo": {
			cfg: cli.Config{Links: []cli.NamedLink{
				{Name: "prod", Repo: "/src/repo"},
			}},
			want: "/src/repo",
		},
		"no repo known": {
			cfg:  cli.Config{},
			want: "",
		},
		"distinct repos": {
			cfg: cli.Config{Repo: "/src/one", Links: []cli.NamedLink{
				{Name: "prod", Repo: "/src/two"},
			}},
			want: "",
		},
	}
	for name, tc := range cases {
		g := newVerbGateway(t, &verbStubBackend{}, tc.cfg)
		rec := do(g, http.MethodPost, "/local/v1/env.harnesses", "{}", true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: env.harnesses = %d: %s", name, rec.Code, rec.Body)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		raw, present := got["repo_path"]
		if tc.want == "" {
			if present {
				t.Errorf("%s: repo_path = %s, want the key omitted", name, raw)
			}
			continue
		}
		var path string
		if err := json.Unmarshal(raw, &path); err != nil {
			t.Fatalf("%s: repo_path = %s: %v", name, raw, err)
		}
		if path != tc.want {
			t.Errorf("%s: repo_path = %q, want %q", name, path, tc.want)
		}
	}
}
