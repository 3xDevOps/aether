// Package macinstall installs one verified file into a directory the
// current user cannot write, through macOS's own administrator dialog.
//
// The dialog comes from `/usr/bin/osascript` running an AppleScript
// `do shell script ... with administrator privileges`, the same route VS
// Code uses to put its `code` command in /usr/local/bin. macOS shows its
// standard password prompt; the password goes to the system's
// authorization service and never passes through Aether. The privileged
// command is a fixed one-liner of system tools - copy, re-hash, chmod,
// rename - with the destination and the release digest baked into its
// text, so what root does is decided before the user is asked and cannot
// be changed after. No Aether code runs as root.
package macinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Request is one file to install with administrator privileges.
type Request struct {
	// Src is the staged file, owned by the current user. It is copied,
	// never executed or renamed: root reads it once into a file of its own.
	Src string
	// Dst is the absolute path of the binary to replace. Its directory is
	// where root stages the copy, so the final step is an atomic rename.
	Dst string
	// SHA256 is the hex digest the copied file must have. It is compared
	// by root against the root-owned copy, which closes the window in
	// which the same user could swap Src while the dialog is up.
	SHA256 string
}

// ErrCanceled reports that the user dismissed the dialog or macOS refused
// the password: nothing was changed.
var ErrCanceled = errors.New("administrator access was not granted")

// ErrNoSession reports that there is no GUI session for the dialog to
// appear in - the gateway is running over SSH, or under a login that has
// no window server.
var ErrNoSession = errors.New("no GUI session to show the macOS authorization dialog in")

// The tools the privileged command runs, by absolute path so nothing is
// looked up on root's PATH. All exist on stock macOS and on Linux, which is
// what lets the command's text be tested there.
const (
	mktemp  = "/usr/bin/mktemp"
	install = "/usr/bin/install"
	openssl = "/usr/bin/openssl"
	chmod   = "/bin/chmod"
	mv      = "/bin/mv"
	rm      = "/bin/rm"
	echo    = "/bin/echo"
	// osascript is what shows the dialog.
	osascript = "/usr/bin/osascript"
	// launchctl says which kind of login session this process is in.
	launchctl = "/bin/launchctl"
)

// HasGUISession reports whether this process runs inside a macOS GUI
// login session, the only place the administrator dialog can appear. A
// gateway started over SSH, or by a launchd daemon, is not; asking there
// would fail after the download, or hang on a dialog nobody sees.
// `launchctl managername` answers "Aqua" for a GUI session and
// "Background" or "System" otherwise.
func HasGUISession() bool {
	out, err := exec.Command(launchctl, "managername").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "Aqua"
}

// Tools lists every program the privileged command runs, for a test that
// wants to skip where one is missing.
func Tools() []string {
	return []string{mktemp, install, openssl, chmod, mv, rm, echo}
}

// ExitChecksumMismatch is the status the privileged command exits with
// when the root-owned copy does not hash to the release digest (EX_DATAERR
// from sysexits.h).
const ExitChecksumMismatch = 65

// sha256Hex matches a lowercase hex SHA-256 digest and nothing else.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ShellCommand returns the POSIX sh command root runs, or an error when
// the request could not be quoted safely. The command:
//
//  1. creates a root-owned 0600 temp file in Dst's directory, which the
//     user cannot write, and arranges to remove it on any exit;
//  2. copies Src into it, still 0600, so a Src swapped for a symlink to a
//     root-only file leaks nothing;
//  3. hashes the root-owned copy and exits ExitChecksumMismatch unless it
//     matches SHA256;
//  4. makes the copy 0755 and renames it over Dst - atomic within one
//     directory, and a running binary keeps its old inode.
func ShellCommand(req Request) (string, error) {
	if err := check(req); err != nil {
		return "", err
	}
	template := filepath.Join(filepath.Dir(req.Dst), ".aether.update.XXXXXX")
	steps := []string{
		"set -e",
		"t=$(" + mktemp + " " + shellQuote(template) + ")",
		"trap " + shellQuote(rm+` -f "$t"`) + " EXIT",
		install + " -m 0600 " + shellQuote(req.Src) + ` "$t"`,
		// LibreSSL prints `SHA256(path)= <hex>`, OpenSSL 3 prints
		// `SHA2-256(path)= <hex>`; the last word is the digest in both.
		"h=$(" + openssl + ` dgst -sha256 "$t")`,
		`[ "${h##* }" = ` + shellQuote(req.SHA256) + " ] || { " +
			echo + " " + shellQuote("copied binary does not match the release checksum") + " >&2; " +
			"exit " + strconv.Itoa(ExitChecksumMismatch) + "; }",
		chmod + ` 0755 "$t"`,
		mv + ` -f "$t" ` + shellQuote(req.Dst),
	}
	return strings.Join(steps, "; "), nil
}

// Script returns the AppleScript osascript runs: the shell command under
// administrator privileges, with prompt as the dialog's explanatory text.
func Script(req Request, prompt string) (string, error) {
	sh, err := ShellCommand(req)
	if err != nil {
		return "", err
	}
	if err := plainText("prompt", prompt); err != nil {
		return "", err
	}
	return `do shell script "` + appleScriptQuote(sh) + `" with prompt "` +
		appleScriptQuote(prompt) + `" with administrator privileges`, nil
}

// check refuses a request whose values could not be quoted safely or
// whose paths do not describe one file replacing another in its own
// directory.
func check(req Request) error {
	paths := []struct{ name, path string }{{"source", req.Src}, {"destination", req.Dst}}
	for _, p := range paths {
		if !filepath.IsAbs(p.path) {
			return fmt.Errorf("%s %q is not an absolute path", p.name, p.path)
		}
		if err := plainText(p.name+" path", p.path); err != nil {
			return err
		}
	}
	if !sha256Hex.MatchString(req.SHA256) {
		return fmt.Errorf("digest %q is not a lowercase hex SHA-256", req.SHA256)
	}
	return nil
}

// plainText refuses control characters, which have no business in a path
// or a prompt and would only make the command harder to read in the
// dialog and the logs.
func plainText(what, s string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character (%q)", what, r)
		}
	}
	return nil
}

// shellQuote single-quotes s for POSIX sh: everything inside single quotes
// is literal, and an embedded quote is closed, escaped and reopened.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleScriptQuote escapes s for an AppleScript string literal, whose only
// special characters are the backslash and the double quote.
func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// run executes one AppleScript through osascript and returns its stderr.
// A variable so tests can stand in for the dialog.
var run = runOsascript

// maxStderr bounds what is kept of osascript's stderr: one error line is
// the norm, and a runaway child must not fill memory.
const maxStderr = 64 << 10

// runOsascript runs the script with a fixed environment. `do shell script`
// hands the caller's environment to the privileged sh, so it gets a
// minimal one: a PATH of system directories, LANG so any message is in a
// form the error classifier reads, and HOME because osascript itself
// runs as the user and looks for its home. Nothing else the user's shell
// set - TMPDIR, DYLD_*, a Homebrew PATH - reaches root.
func runOsascript(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, osascript, "-e", script)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: maxStderr}
	// A cancelled context kills osascript; the privileged child, if it is
	// already running, belongs to the system's authorization trampoline
	// and finishes on its own. WaitDelay stops Wait from hanging on a
	// pipe that child may still hold.
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return stderr.Bytes(), err
}

// Install runs the privileged command through the administrator dialog and
// classifies the outcome: ErrCanceled, ErrNoSession, or the real error
// with osascript's own message.
func Install(ctx context.Context, req Request, prompt string) error {
	script, err := Script(req, prompt)
	if err != nil {
		return err
	}
	stderr, err := run(ctx, script)
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(stderr))
	if ctx.Err() != nil {
		return fmt.Errorf("the authorization dialog was cancelled with the request: %w", ctx.Err())
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		// osascript could not be started at all: missing, or not executable.
		return fmt.Errorf("run %s: %w", osascript, err)
	}
	if msg == "" {
		msg = err.Error()
	}
	switch appleScriptErrorNumber(msg) {
	case -128:
		// "User canceled." Also what macOS reports once it gives up on
		// repeated wrong passwords.
		return fmt.Errorf("%w: %s", ErrCanceled, msg)
	case -1713:
		// "No user interaction allowed."
		return fmt.Errorf("%w: %s", ErrNoSession, msg)
	}
	return fmt.Errorf("osascript: %s", msg)
}

// trailingNumber matches the "(<n>)" osascript appends to an execution
// error: the AppleScript error number, or the exit status of the shell
// command. Only the number is read; the text before it is localized.
var trailingNumber = regexp.MustCompile(`\((-?[0-9]+)\)\s*$`)

// appleScriptErrorNumber reads the error number off the last line of
// osascript's stderr, or 0 when there is none.
func appleScriptErrorNumber(stderr string) int {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	m := trailingNumber.FindStringSubmatch(lines[len(lines)-1])
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// limitedWriter keeps the first n bytes and drops the rest without
// failing the writer, so a chatty child does not turn into an I/O error.
type limitedWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
