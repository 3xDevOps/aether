package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchHelp(t *testing.T) {
	if err := dispatch(nil); err != nil {
		t.Fatalf("bare aether: %v", err)
	}
	help := helpText()
	for _, name := range []string{"daemon", "version", "init", "link", "run", "attach", "terminal", "runs", "pull", "workspace", "image", "member", "invite", "gui", "gui build", "profile", "kill", "pause", "resume", "inject", "close", "relaunch", "inbox", "who", "handoff", "timeline", "cost", "budget", "template", "schedule", "protect", "unprotect"} {
		if !strings.Contains(help, name) {
			t.Errorf("help missing %q:\n%s", name, help)
		}
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

func TestAbsoluteRepo(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	got, err := absoluteRepo(".")
	if err != nil {
		t.Fatalf("absoluteRepo(.): %v", err)
	}
	if want := filepath.Clean(cwd); got != want {
		t.Errorf("absoluteRepo(.) = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("absoluteRepo(.) = %q, want absolute path", got)
	}

	got, err = absoluteRepo("")
	if err != nil {
		t.Fatalf("absoluteRepo(empty): %v", err)
	}
	if got != "" {
		t.Errorf("absoluteRepo(empty) = %q, want empty", got)
	}
}

func TestSteerUsage(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"kill"}, "usage: aether kill <run-id>"},
		{[]string{"pause"}, "usage: aether pause <run-id>"},
		{[]string{"resume"}, "usage: aether resume <run-id>"},
		{[]string{"inject"}, "usage: aether inject <run-id> <message...>"},
		{[]string{"inject", "run-1"}, "usage: aether inject <run-id> <message...>"},
		{[]string{"close"}, "usage: aether close <run-id> --outcome merged|abandoned"},
		{[]string{"close", "run-1"}, "usage: aether close <run-id> --outcome merged|abandoned"},
		{[]string{"relaunch"}, "usage: aether relaunch <run-id>"},
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

	err := copyRawStreams(stream, strings.NewReader(""), &output)

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

	if err := copyRawStreams(stream, input, &output); err != nil {
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
