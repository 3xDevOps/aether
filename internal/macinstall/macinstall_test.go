//go:build !windows

// The command text is built and run on Linux too, which is where CI
// exercises it; Windows paths have no place in it and self-update is
// refused there before this package is reached.

package macinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func request(src, dst string, body []byte) Request {
	sum := sha256.Sum256(body)
	return Request{Src: src, Dst: dst, SHA256: hex.EncodeToString(sum[:])}
}

func TestShellCommandQuotesEveryValue(t *testing.T) {
	req := Request{
		Src:    `/Users/a b/Library/Caches/it's "odd"\$HOME/aether.update-1`,
		Dst:    "/usr/local/bin/aether",
		SHA256: strings.Repeat("ab", 32),
	}
	got, err := ShellCommand(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `set -e; ` +
		`t=$(/usr/bin/mktemp '/usr/local/bin/.aether.update.XXXXXX'); ` +
		`trap '/bin/rm -f "$t"' EXIT; ` +
		`/usr/bin/install -m 0600 '/Users/a b/Library/Caches/it'\''s "odd"\$HOME/aether.update-1' "$t"; ` +
		`h=$(/usr/bin/openssl dgst -sha256 "$t"); ` +
		`[ "${h##* }" = '` + req.SHA256 + `' ] || { /bin/echo 'copied binary does not match the release checksum' >&2; exit 65; }; ` +
		`/bin/chmod 0755 "$t"; ` +
		`/bin/mv -f "$t" '/usr/local/bin/aether'`
	if got != want {
		t.Fatalf("command =\n%s\nwant\n%s", got, want)
	}
}

func TestScriptEscapesForAppleScript(t *testing.T) {
	req := Request{Src: `/tmp/say "hi"\now`, Dst: "/usr/local/bin/aether", SHA256: strings.Repeat("0", 64)}
	got, err := Script(req, `Aether wants to replace "aether".`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, `do shell script "set -e; `) {
		t.Fatalf("script = %q, want it to open the shell command", got)
	}
	if !strings.HasSuffix(got, `" with prompt "Aether wants to replace \"aether\"." with administrator privileges`) {
		t.Fatalf("script = %q, want the escaped prompt and the privileges clause", got)
	}
	// The source path's quote and backslash both survive as AppleScript
	// escapes, so the shell sees the original characters.
	if !strings.Contains(got, `'/tmp/say \"hi\"\\now'`) {
		t.Fatalf("script = %q, want the source path escaped for AppleScript", got)
	}
}

func TestScriptRejectsUnsafeInput(t *testing.T) {
	ok := Request{Src: "/tmp/a", Dst: "/usr/local/bin/aether", SHA256: strings.Repeat("0", 64)}
	cases := map[string]Request{
		"relative source": {Src: "tmp/a", Dst: ok.Dst, SHA256: ok.SHA256},
		"relative dest":   {Src: ok.Src, Dst: "aether", SHA256: ok.SHA256},
		"newline in path": {Src: "/tmp/a\nb", Dst: ok.Dst, SHA256: ok.SHA256},
		"short digest":    {Src: ok.Src, Dst: ok.Dst, SHA256: strings.Repeat("0", 63)},
		"upper digest":    {Src: ok.Src, Dst: ok.Dst, SHA256: strings.Repeat("A", 64)},
	}
	for name, req := range cases {
		if _, err := Script(req, "p"); err == nil {
			t.Errorf("%s: accepted %+v", name, req)
		}
	}
	if _, err := Script(ok, "line one\nline two"); err == nil {
		t.Error("accepted a prompt with a newline")
	}
	if _, err := Script(ok, "fine"); err != nil {
		t.Errorf("rejected a valid request: %v", err)
	}
}

// stubRun replaces the dialog for one test.
func stubRun(t *testing.T, fn func(context.Context, string) ([]byte, error)) {
	t.Helper()
	old := run
	t.Cleanup(func() { run = old })
	run = fn
}

// exitStatus fabricates the error exec returns for a child that exited
// non-zero; the classifier must not depend on the status itself.
func exitStatus(t *testing.T) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit 1").Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Skip("no /bin/sh to produce an exit status")
	}
	return err
}

func TestInstallClassifiesOsascriptErrors(t *testing.T) {
	req := Request{Src: "/tmp/a", Dst: "/usr/local/bin/aether", SHA256: strings.Repeat("0", 64)}
	cases := []struct {
		stderr string
		want   error
	}{
		{"41:63: execution error: User canceled. (-128)\n", ErrCanceled},
		// Localized text, same number.
		{"41:63: execution error: L’utilisateur a annulé. (-128)", ErrCanceled},
		{"execution error: No user interaction allowed. (-1713)", ErrNoSession},
		{"execution error: copied binary does not match the release checksum (65)", nil},
		{"something without a number", nil},
	}
	for _, tc := range cases {
		stubRun(t, func(context.Context, string) ([]byte, error) {
			return []byte(tc.stderr), exitStatus(t)
		})
		err := Install(context.Background(), req, "p")
		if err == nil {
			t.Fatalf("%q: no error", tc.stderr)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%q: err = %v, want %v", tc.stderr, err, tc.want)
		}
		if tc.want == nil && (errors.Is(err, ErrCanceled) || errors.Is(err, ErrNoSession)) {
			t.Errorf("%q: classified as %v", tc.stderr, err)
		}
		if !strings.Contains(err.Error(), strings.TrimSpace(tc.stderr)) {
			t.Errorf("%q: err = %v, want osascript's own text kept", tc.stderr, err)
		}
	}
}

func TestInstallPassesTheScript(t *testing.T) {
	req := Request{Src: "/tmp/a", Dst: "/usr/local/bin/aether", SHA256: strings.Repeat("0", 64)}
	want, err := Script(req, "why")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	stubRun(t, func(_ context.Context, script string) ([]byte, error) {
		got = script
		return nil, nil
	})
	if err := Install(context.Background(), req, "why"); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("script = %q, want %q", got, want)
	}
}

func TestInstallReportsACancelledRequest(t *testing.T) {
	req := Request{Src: "/tmp/a", Dst: "/usr/local/bin/aether", SHA256: strings.Repeat("0", 64)}
	ctx, cancel := context.WithCancel(context.Background())
	stubRun(t, func(context.Context, string) ([]byte, error) {
		cancel()
		return []byte("signal: killed"), exitStatus(t)
	})
	err := Install(ctx, req, "p")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's cancellation", err)
	}
}

// The command's text runs unchanged on Linux, where the test can execute
// it as an ordinary user against a directory standing in for
// /usr/local/bin. Ownership is root's doing and is not asserted here.
func TestShellCommandInstallsAVerifiedCopy(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("new release binary")
	src := filepath.Join(t.TempDir(), "staged")
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := ShellCommand(request(src, dst, body))
	if err != nil {
		t.Fatal(err)
	}
	if out, runErr := exec.Command("/bin/sh", "-c", cmd).CombinedOutput(); runErr != nil {
		t.Fatalf("sh: %v: %s", runErr, out)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("dst = %q, want the staged bytes", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
	assertNoLeftovers(t, dir)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the staged file must be left for the caller to remove: %v", err)
	}
}

func TestShellCommandRefusesAMismatchedCopy(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "staged")
	if err := os.WriteFile(src, []byte("swapped after verification"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := ShellCommand(request(src, dst, []byte("what the release hashes to")))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != ExitChecksumMismatch {
		t.Fatalf("sh: err = %v, want exit %d: %s", err, ExitChecksumMismatch, out)
	}
	if !strings.Contains(string(out), "does not match the release checksum") {
		t.Fatalf("stderr = %q, want the mismatch line", out)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("dst = %q, want it untouched", got)
	}
	assertNoLeftovers(t, dir)
}

func TestShellCommandFailsBeforeTouchingDstWithoutASource(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "aether")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, err := ShellCommand(Request{Src: filepath.Join(dir, "missing"), Dst: dst, SHA256: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput(); err == nil {
		t.Fatal("installed from a missing source")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "old" {
		t.Fatalf("dst = %q, want it untouched", got)
	}
	assertNoLeftovers(t, dir)
}

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range append(Tools(), "/bin/sh") {
		if _, err := os.Stat(tool); err != nil {
			t.Skipf("%s is not installed here", tool)
		}
	}
}

func assertNoLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".aether.update.") {
			t.Fatalf("temp file %s left behind", e.Name())
		}
	}
}
