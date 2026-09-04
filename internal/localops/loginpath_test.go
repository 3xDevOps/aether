//go:build !windows

package localops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// loginPathSetup isolates one AdoptLoginPath call: PATH starts as the bare
// desktop-session PATH plus the two system-wide fallback folders (so the
// machine's real /usr/local/bin and /opt/homebrew/bin are never counted as
// added), HOME is a fresh directory with only .local/bin created, and
// SHELL is a stub script that receives the login-shell arguments.
func loginPathSetup(t *testing.T, shellBody string) (home string) {
	t.Helper()
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin")
	shell := filepath.Join(t.TempDir(), "shell.sh")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"+shellBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	return home
}

// loginShellStub checks the arguments and the guard variable a login
// shell gets, prints banner noise the way interactive rc files do, and
// runs the command with PATH set to path. Running the command through
// /bin/sh -c keeps the test clear of the real /etc/profile a -l shell
// would source.
func loginShellStub(path string) string {
	return `[ "$1" = -l ] && [ "$2" = -i ] && [ "$3" = -c ] || { echo "bad args: $*" >&2; exit 2; }
[ "$AETHER_RESOLVING_PATH" = 1 ] || { echo "AETHER_RESOLVING_PATH not set" >&2; exit 3; }
echo "Welcome to the stub shell __AETHER_PATH_BEGIN__decoy__AETHER_PATH_END__"
PATH='` + path + `' /bin/sh -c "$4"
echo "goodbye"
`
}

func TestAdoptLoginPathPutsShellEntriesFirstThenFallbacks(t *testing.T) {
	home := loginPathSetup(t, loginShellStub("/stub/one::/usr/bin:/stub/two:/stub/one"))

	added, err := AdoptLoginPath(context.Background())
	if err != nil {
		t.Fatalf("AdoptLoginPath: %v", err)
	}
	local := filepath.Join(home, ".local", "bin")
	wantAdded := []string{"/stub/one", "/stub/two", local}
	if !reflect.DeepEqual(added, wantAdded) {
		t.Fatalf("added = %v, want %v", added, wantAdded)
	}
	// The shell's order wins: /usr/bin moves ahead of /bin because the
	// shell listed it after /stub/one and never listed /bin.
	want := []string{"/stub/one", "/usr/bin", "/stub/two", "/bin", "/usr/local/bin", "/opt/homebrew/bin", local}
	if got := SearchedDirs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PATH after = %v, want %v", got, want)
	}
	if bun := filepath.Join(home, ".bun", "bin"); strings.Contains(os.Getenv("PATH"), bun) {
		t.Errorf("PATH gained %s, which does not exist", bun)
	}
}

func TestAdoptLoginPathAddsNothingWhenShellAgrees(t *testing.T) {
	loginPathSetup(t, loginShellStub("/usr/bin:/bin"))
	// The fallback the fixture creates is already listed, so nothing is new.
	t.Setenv("PATH", os.Getenv("PATH")+":"+filepath.Join(os.Getenv("HOME"), ".local", "bin"))
	before := os.Getenv("PATH")

	added, err := AdoptLoginPath(context.Background())
	if err != nil {
		t.Fatalf("AdoptLoginPath: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v, want none", added)
	}
	if os.Getenv("PATH") != before {
		t.Fatalf("PATH changed to %q", os.Getenv("PATH"))
	}
}

func TestAdoptLoginPathTimeoutKillsRcChildAndAppliesFallbacks(t *testing.T) {
	// The sleeper is a child of the shell, not the shell itself, so the
	// deadline has to reach the whole process group or the child is left
	// behind holding stdout after the shell is gone.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	home := loginPathSetup(t, "sleep 30 &\necho $! > '"+pidFile+"'\nwait\n")
	// The deadline lands once the child exists, so a slow process start
	// under a loaded test run cannot make the kill happen before it.
	ctx := cancelOnceWritten(t, pidFile)

	added, err := AdoptLoginPath(ctx)
	if err == nil {
		t.Fatal("AdoptLoginPath: want a timeout error")
	}
	if !strings.Contains(err.Error(), "did not answer in time") {
		t.Fatalf("error = %v, want the timeout named", err)
	}
	want := []string{filepath.Join(home, ".local", "bin")}
	if !reflect.DeepEqual(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
	assertProcessGone(t, pidFile)
}

// assertProcessGone fails unless the process whose pid the stub wrote to
// pidFile has exited shortly after AdoptLoginPath returned.
func assertProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the stub never wrote its child's pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("child pid %q: %v", data, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the rc-file child %d outlived the shell", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cancelOnceWritten returns a context that is cancelled as soon as file
// exists (or after 5s), so a test cuts the stub shell off at a point in
// its progress instead of at a wall-clock deadline a slow process start
// could beat.
func cancelOnceWritten(t *testing.T, file string) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for stop := time.Now().Add(5 * time.Second); time.Now().Before(stop); time.Sleep(10 * time.Millisecond) {
			if _, statErr := os.Stat(file); statErr == nil {
				break
			}
		}
		cancel()
	}()
	return ctx
}

func TestAdoptLoginPathKeepsPrintedPathWhenShellHangsOnExit(t *testing.T) {
	// bash runs ~/.bash_logout and zsh ~/.zlogout after the command printed:
	// the stub writes its pid once the PATH is out, then hangs like one.
	pidFile := filepath.Join(t.TempDir(), "shell.pid")
	home := loginPathSetup(t, loginShellStub("/stub/late")+"echo $$ > '"+pidFile+"'\nsleep 30\n")
	ctx := cancelOnceWritten(t, pidFile)

	added, err := AdoptLoginPath(ctx)
	if err != nil {
		t.Fatalf("AdoptLoginPath: %v", err)
	}
	want := []string{"/stub/late", filepath.Join(home, ".local", "bin")}
	if !reflect.DeepEqual(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
	assertProcessGone(t, pidFile)
}

func TestAdoptLoginPathRetriesAsPlainLoginShell(t *testing.T) {
	// An rc file that execs tmux or exits early leaves the interactive
	// run without a PATH; the profile entries still come from -l alone.
	home := loginPathSetup(t, `for arg in "$@"; do [ "$arg" = -i ] && exit 1; done
[ "$1" = -l ] && [ "$2" = -c ] || { echo "bad args: $*" >&2; exit 2; }
PATH='/stub/profile' /bin/sh -c "$3"
`)

	added, err := AdoptLoginPath(context.Background())
	if err != nil {
		t.Fatalf("AdoptLoginPath: %v", err)
	}
	want := []string{"/stub/profile", filepath.Join(home, ".local", "bin")}
	if !reflect.DeepEqual(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
}

func TestAdoptLoginPathDropsEntriesThatAreNotAbsolute(t *testing.T) {
	// A shell whose double quotes do not expand $PATH prints it literally.
	home := loginPathSetup(t, "printf '__AETHER_PATH_BEGIN__$PATH:rel/bin:.:/stub/abs__AETHER_PATH_END__\\n'\n")

	added, err := AdoptLoginPath(context.Background())
	if err != nil {
		t.Fatalf("AdoptLoginPath: %v", err)
	}
	want := []string{"/stub/abs", filepath.Join(home, ".local", "bin")}
	if !reflect.DeepEqual(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
}

func TestAdoptLoginPathShellFailureStillAppliesFallbacks(t *testing.T) {
	home := loginPathSetup(t, "echo 'rc file exploded' >&2\nexit 1\n")

	added, err := AdoptLoginPath(context.Background())
	if err == nil {
		t.Fatal("AdoptLoginPath: want an error from the failing shell")
	}
	// The interactive run is the one reported: it is the one the user
	// has to fix.
	if !strings.Contains(err.Error(), "read PATH from the login shell") || !strings.Contains(err.Error(), "-l -i -c: exit status 1") {
		t.Fatalf("error = %v, want the -l -i run named", err)
	}
	want := []string{filepath.Join(home, ".local", "bin")}
	if !reflect.DeepEqual(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
}

func TestSearchedDirsDropsEmptyEntries(t *testing.T) {
	t.Setenv("PATH", ":/usr/bin::/bin:")
	if got, want := SearchedDirs(), []string{"/usr/bin", "/bin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchedDirs = %v, want %v", got, want)
	}
	t.Setenv("PATH", "")
	if got := SearchedDirs(); got == nil || len(got) != 0 {
		t.Fatalf("SearchedDirs on empty PATH = %#v, want an empty non-nil list", got)
	}
}
