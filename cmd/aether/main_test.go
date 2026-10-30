package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestDispatchHelp(t *testing.T) {
	out, err := captureStdout(t, func() error { return dispatch(nil) })
	if err != nil {
		t.Fatalf("bare aether: %v", err)
	}
	for _, name := range []string{"daemon", "version", "init", "link", "session", "attach", "terminal", "sessions", "pull", "workspace", "member", "invite", "gui", "gui build", "profile", "kill", "delete", "archive", "unarchive", "pause", "resume", "message", "close", "reopen", "who", "handoff", "timeline", "cost", "budget", "template", "schedule", "protect", "unprotect"} {
		if !strings.Contains(out, name) {
			t.Errorf("help missing %q:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "reopen a finished session that still keeps its container") {
		t.Errorf("relaunch help = %q, want the kept-container wording", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if tail := strings.Join(lines[len(lines)-2:], "\n"); !strings.Contains(tail, "aether status") || !strings.Contains(tail, "coding agents: aether help agents") {
		t.Errorf("help ends with %q, want where to start and the agents guide", tail)
	}
}

// Every command and subcommand form answers --help on stdout without
// reaching the linked server, and aether help <command> prints the same.
func TestEveryCommandAnswersHelp(t *testing.T) {
	testhome.Isolate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var dials atomic.Int32
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			dials.Add(1)
			_ = conn.Close()
		}
	}()
	if err = cli.Save(cli.Config{Addr: ln.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	for _, c := range commands {
		paths := []string{c.name}
		for _, line := range strings.Split(c.usage, "\n") {
			words := strings.Fields(line)
			if len(words) < 2 || words[0] != "aether" {
				continue
			}
			n := 1
			for n < len(words) && !strings.ContainsAny(words[n][:1], "<[-|\"'.") {
				n++
			}
			if path := strings.Join(words[1:n], " "); !slices.Contains(paths, path) {
				paths = append(paths, path)
			}
		}
		for _, path := range paths {
			args := strings.Fields(path)
			out, herr := captureStdout(t, func() error { return dispatch(append(args, "--help")) })
			if herr != nil {
				t.Errorf("aether %s --help: %v", path, herr)
				continue
			}
			if !strings.HasPrefix(out, "usage: aether "+path) {
				t.Errorf("aether %s --help prints %q, want its usage first", path, out)
			}
			if slices.Contains(flagSubcommands, path) && !strings.Contains(out, "\nflags:\n  -") {
				t.Errorf("aether %s --help lists no flags:\n%s", path, out)
			}
			if path != c.name {
				continue
			}
			for _, want := range append(strings.Split(c.usage, "\n"), c.short, "\nexample:\n  aether "+c.name) {
				if !strings.Contains(out, want) {
					t.Errorf("aether %s --help lacks %q:\n%s", c.name, want, out)
				}
			}
			if c.flags && !strings.Contains(out, "\nflags:\n") {
				t.Errorf("aether %s --help lists no flags:\n%s", c.name, out)
			}
			viaHelp, herr := captureStdout(t, func() error { return dispatch([]string{"help", c.name}) })
			if herr != nil || viaHelp != out {
				t.Errorf("aether help %s = %q, %v; want the --help output", c.name, viaHelp, herr)
			}
		}
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("--help dialed the linked server %d times", n)
	}
}

func TestSubcommandHelpShowsFlagDefaults(t *testing.T) {
	for path, wants := range map[string][]string{
		"template save":              {"-mode string", `(default "background")`},
		"workspace mirror configure": {"-auth string", `(default "public")`},
	} {
		out, err := captureStdout(t, func() error { return dispatch(append(strings.Fields(path), "--help")) })
		if err != nil {
			t.Fatalf("aether %s --help: %v", path, err)
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("aether %s --help lacks %q:\n%s", path, want, out)
			}
		}
	}
}

func TestFlagSetHelpAndErrors(t *testing.T) {
	out, err := captureStdout(t, func() error { return dispatch([]string{"sessions", "--json", "-h"}) })
	if err != nil || !strings.Contains(out, "\nflags:\n  -archived") {
		t.Errorf("aether sessions --json -h = %q, %v; want the flags and no error", out, err)
	}
	err = dispatch([]string{"sessions", "--nope"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -nope; see aether sessions --help") {
		t.Errorf("aether sessions --nope = %v, want the flag error and the help command", err)
	}
	out, err = captureStdout(t, func() error { return dispatch([]string{"help", "agents"}) })
	if err != nil || !strings.HasPrefix(out, "# ") {
		t.Errorf("aether help agents = %q, %v; want docs/agents.md", out, err)
	}
}

func TestFlagParseErrorKeepsCause(t *testing.T) {
	err := dispatch([]string{"template", "save", "audit", "--agent", "claude", "--task", "fix", "--budget", "nope"})
	if err == nil || !strings.Contains(err.Error(), `invalid value "nope" for flag -budget`) {
		t.Errorf("template save --budget nope = %v, want the invalid value error", err)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	err := dispatch([]string{"nope"})
	if err == nil {
		t.Fatal("unknown command succeeded")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error = %v, want unknown command", err)
	}
}

func TestDispatchVersion(t *testing.T) {
	if err := dispatch([]string{"version"}); err != nil {
		t.Fatalf("version: %v", err)
	}
}
func TestParseLeadingArgSupportsBothFlagOrders(t *testing.T) {
	for _, args := range [][]string{
		{"task", "--agent", "claude"},
		{"--agent", "claude", "task"},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		agent := fs.String("agent", "", "")
		got, err := parseLeadingArg(fs, args)
		if err != nil {
			t.Fatalf("parseLeadingArg(%v): %v", args, err)
		}
		if got != "task" || *agent != "claude" {
			t.Errorf("parseLeadingArg(%v) = (%q, %q), want (task, claude)", args, got, *agent)
		}
	}
}

func TestAbsolutePath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	got, err := absolutePath(".")
	if err != nil {
		t.Fatalf("absolutePath(.): %v", err)
	}
	if want := filepath.Clean(cwd); got != want {
		t.Errorf("absolutePath(.) = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("absolutePath(.) = %q, want absolute path", got)
	}

	got, err = absolutePath("")
	if err != nil {
		t.Fatalf("absolutePath(empty): %v", err)
	}
	if got != "" {
		t.Errorf("absolutePath(empty) = %q, want empty", got)
	}
}

func TestSteerUsage(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"kill"}, "usage: aether kill <session-id>"},
		{[]string{"delete"}, "usage: aether delete <session-id>"},
		{[]string{"delete", "run-1", "run-2"}, "usage: aether delete <session-id>"},
		{[]string{"archive"}, "usage: aether archive <session-id>"},
		{[]string{"archive", "run-1", "run-2"}, "usage: aether archive <session-id>"},
		{[]string{"unarchive"}, "usage: aether unarchive <session-id>"},
		{[]string{"pause"}, "usage: aether pause <session-id>"},
		{[]string{"resume"}, "usage: aether resume <session-id>"},
		{[]string{"message"}, "usage: aether message <session-id> <message...>"},
		{[]string{"message", "run-1"}, "usage: aether message <session-id> <message...>"},
		{[]string{"close"}, "usage: aether close <session-id> --outcome merged|abandoned"},
		{[]string{"close", "run-1"}, "usage: aether close <session-id> --outcome merged|abandoned"},
		{[]string{"reopen"}, "usage: aether reopen <session-id>"},
		{[]string{"env"}, "usage: aether env save|reset"},
		{[]string{"env", "save", "extra"}, "usage: aether env save|reset"},
		{[]string{"env", "reset", "extra"}, "usage: aether env save|reset"},
	}
	for _, tc := range cases {
		err := dispatch(tc.args)
		if err == nil {
			t.Errorf("%v: succeeded, want usage error", tc.args)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestCopyRawStreamsWaitsForRemoteResultAfterInputEOF(t *testing.T) {
	wantErr := errors.New("remote process exited with status 1")
	writeClosed := make(chan struct{})
	stream := &rawTestStream{
		Reader: &afterSignalReader{
			ready: writeClosed,
			Reader: io.MultiReader(
				strings.NewReader("setup failed\n"),
				errorReader{err: wantErr},
			),
		},
		Writer: io.Discard,
		closeWrite: func() error {
			close(writeClosed)
			return nil
		},
	}
	var output bytes.Buffer

	err := copyRawStreams(stream, strings.NewReader(""), &output, 0)

	if !errors.Is(err, wantErr) {
		t.Fatalf("copy error = %v, want %v", err, wantErr)
	}
	if got := output.String(); got != "setup failed\n" {
		t.Fatalf("output = %q, want setup failure", got)
	}
}

func TestCopyRawStreamsReturnsAfterSuccessfulRemoteClose(t *testing.T) {
	input := &blockingReadCloser{
		closed:   make(chan struct{}),
		closeErr: errors.New("input close error"),
	}
	streamClosed := false
	stream := &rawTestStream{
		Reader: strings.NewReader("remote complete\n"),
		Writer: io.Discard,
		close: func() error {
			streamClosed = true
			return nil
		},
	}
	var output bytes.Buffer

	if err := copyRawStreams(stream, input, &output, 0); err != nil {
		t.Fatalf("copy error = %v, want nil", err)
	}
	if got := output.String(); got != "remote complete\n" {
		t.Fatalf("output = %q, want remote output", got)
	}
	if !streamClosed {
		t.Fatal("remote stream was not closed")
	}
	select {
	case <-input.closed:
	default:
		t.Fatal("blocking input was not closed")
	}
}

type rawTestStream struct {
	io.Reader
	io.Writer
	closeWrite func() error
	close      func() error
}

func (s *rawTestStream) CloseWrite() error {
	if s.closeWrite == nil {
		return nil
	}
	return s.closeWrite()
}

func (s *rawTestStream) Close() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}

type afterSignalReader struct {
	ready <-chan struct{}
	io.Reader
}

func (r *afterSignalReader) Read(p []byte) (int, error) {
	<-r.ready
	return r.Reader.Read(p)
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type blockingReadCloser struct {
	closed   chan struct{}
	closeErr error
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *blockingReadCloser) Close() error {
	close(r.closed)
	return r.closeErr
}
