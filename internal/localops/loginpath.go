package localops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// Sentinels bracket the PATH the login shell prints, so banners and
// warnings from interactive rc files never end up parsed as directories.
const (
	loginPathBegin = "__AETHER_PATH_BEGIN__"
	loginPathEnd   = "__AETHER_PATH_END__"
)

// loginPathMu serializes the probe and the PATH write: the gateway calls
// AdoptLoginPath from concurrent requests.
var loginPathMu sync.Mutex

// AdoptLoginPath widens this process's PATH so a gateway started from a
// desktop session (launchd, a .desktop launcher) finds the same tools a
// terminal does: coding agents installed through a shell profile live in
// folders the desktop PATH never lists. The login shell's entries come
// first, in the shell's own order, so a profile prepend that shadows a
// system folder keeps winning; the current entries the shell did not
// list follow, then the common tool folders that exist on disk. The
// caller bounds ctx; a shell that hangs or fails is reported through the
// error, and the fallback folders are still applied, so the returned list
// is always the folders the original PATH lacked. Windows is a no-op: GUI
// apps there inherit the user's PATH from the registry, and there is no
// login shell to ask.
func AdoptLoginPath(ctx context.Context) (added []string, err error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	loginPathMu.Lock()
	defer loginPathMu.Unlock()

	current := SearchedDirs()
	had := make(map[string]bool, len(current))
	for _, dir := range current {
		had[dir] = true
	}
	var merged []string
	seen := make(map[string]bool, len(current))
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		merged = append(merged, dir)
		if !had[dir] {
			added = append(added, dir)
		}
	}

	entries, shellErr := loginShellPath(ctx)
	for _, dir := range entries {
		add(dir)
	}
	for _, dir := range current {
		add(dir)
	}
	for _, dir := range fallbackToolDirs() {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			add(dir)
		}
	}
	if !slices.Equal(merged, current) {
		if setErr := os.Setenv("PATH", strings.Join(merged, string(os.PathListSeparator))); setErr != nil {
			return nil, fmt.Errorf("localops: set PATH: %w", setErr)
		}
	}
	if shellErr != nil {
		return added, fmt.Errorf("read PATH from the login shell: %w", shellErr)
	}
	return added, nil
}

// SearchedDirs is the current PATH as a list of folders, empty entries
// dropped: what exec.LookPath searches right now.
func SearchedDirs() []string {
	dirs := []string{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// loginShellPath asks $SHELL (or /bin/sh) for its PATH as a login,
// interactive shell so every profile and rc file contributes, and once
// more as a plain login shell when the interactive run printed nothing
// (an rc file that execs tmux or exits early still leaves the profile
// entries). The first run's error is the one reported: it names the
// shell the user has to fix.
func loginShellPath(ctx context.Context) ([]string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	script := "printf '\\n" + loginPathBegin + "%s" + loginPathEnd + "\\n' \"$PATH\""
	path, err := runLoginShell(ctx, shell, "-l", "-i", "-c", script)
	if err != nil && ctx.Err() == nil {
		if retried, retryErr := runLoginShell(ctx, shell, "-l", "-c", script); retryErr == nil {
			path, err = retried, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return absoluteDirs(filepath.SplitList(path)), nil
}

// runLoginShell runs one shell invocation and returns the PATH it printed
// between the sentinels. Stdin is closed so an rc file that prompts cannot
// wait for input, and AETHER_RESOLVING_PATH=1 lets rc files skip work
// meant for a real terminal.
func runLoginShell(ctx context.Context, shell string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Env = append(os.Environ(), "AETHER_RESOLVING_PATH=1")
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	// The deadline must reach an rc-file child stuck on the network as well
	// as the shell itself, or the child outlives the shell holding stdout.
	detachCommand(cmd)
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	// The PATH is complete once the end sentinel is printed, however the
	// shell exits afterwards (a slow .zlogout or an EXIT trap still counts).
	if path, ok := betweenSentinels(out.String()); ok {
		return path, nil
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s did not answer in time: %w", shell, ctx.Err())
	}
	if runErr != nil {
		return "", fmt.Errorf("%s %s: %w", shell, strings.Join(args[:len(args)-1], " "), runErr)
	}
	return "", errors.New(shell + " printed no PATH")
}

// absoluteDirs keeps only absolute folders. A shell whose double quotes do
// not expand $PATH (nushell, elvish) prints the literal text, and
// exec.LookPath cannot use a relative entry either, so nothing is lost.
func absoluteDirs(dirs []string) []string {
	var kept []string
	for _, dir := range dirs {
		if filepath.IsAbs(dir) {
			kept = append(kept, dir)
		}
	}
	return kept
}

// betweenSentinels returns the text between the last begin sentinel and
// the end sentinel that follows it.
func betweenSentinels(s string) (string, bool) {
	start := strings.LastIndex(s, loginPathBegin)
	if start < 0 {
		return "", false
	}
	rest := s[start+len(loginPathBegin):]
	end := strings.Index(rest, loginPathEnd)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// fallbackToolDirs are the folders the coding agents' installers use,
// appended when they exist even if the shell could not be asked.
func fallbackToolDirs() []string {
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".bun", "bin"),
		)
	}
	return dirs
}
