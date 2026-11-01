package coordcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

func TestParseSudo(t *testing.T) {
	cases := []struct {
		args []string
		want protocol.RunSudoParams
		err  string
	}{
		{args: []string{"apt-get", "install", "-y", "jq"}, want: protocol.RunSudoParams{Argv: []string{"apt-get", "install", "-y", "jq"}}},
		{args: []string{"-E", "-H", "--", "-x"}, want: protocol.RunSudoParams{Argv: []string{"-x"}}},
		{args: []string{"-nEu", "postgres", "psql"}, want: protocol.RunSudoParams{User: "postgres", NonInteractive: true, Argv: []string{"psql"}}},
		{args: []string{"-upostgres", "-", "x"}, want: protocol.RunSudoParams{User: "postgres", Argv: []string{"-", "x"}}},
		{args: []string{"-i"}, err: "-i is not supported in an Aether Default session"},
		{args: []string{"-Es", "id"}, err: "-s is not supported in an Aether Default session"},
		{args: []string{"--preserve-env=PATH", "id"}, err: "--preserve-env is not supported in an Aether Default session"},
		{args: []string{"-u"}, err: sudoUsage},
		{args: []string{"-n"}, err: sudoUsage},
		{args: nil, err: sudoUsage},
	}
	for _, tc := range cases {
		got, err := parseSudo(tc.args)
		switch {
		case tc.err != "" && (err == nil || err.Error() != tc.err):
			t.Errorf("parseSudo(%q) error = %v, want %q", tc.args, err, tc.err)
		case tc.err == "" && (err != nil || got.User != tc.want.User || got.NonInteractive != tc.want.NonInteractive || !slices.Equal(got.Argv, tc.want.Argv)):
			t.Errorf("parseSudo(%q) = %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
}

// fakeSudo answers run.sudo the way the scheduler does, from one script.
type fakeSudo struct {
	params  chan protocol.RunSudoParams
	stdin   chan []byte
	ended   chan error
	respond func(ctx context.Context, stdout, stderr io.Writer) (int, error)
}

func (f *fakeSudo) Sudo(ctx context.Context, _ domain.RunID, p protocol.RunSudoParams, stdin []byte, stdout, stderr io.Writer) (int, error) {
	f.params <- p
	f.stdin <- stdin
	code, err := f.respond(ctx, stdout, stderr)
	f.ended <- err
	return code, err
}

func sudoSocket(t *testing.T, runner coord.SudoRunner) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "as-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	db, err := storetest.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	service, err := coord.New(coord.Config{Dir: filepath.Join(dir, "coord"), Store: db, Mail: db, Bus: bus, Peers: hookPeers{}, Sudo: runner})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if err = service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	mount, err := service.Provision(t.Context(), "run-sudo", nil)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(mount, coordtransport.SocketName)
}

func newFakeSudo(respond func(ctx context.Context, stdout, stderr io.Writer) (int, error)) *fakeSudo {
	return &fakeSudo{params: make(chan protocol.RunSudoParams, 1), stdin: make(chan []byte, 1), ended: make(chan error, 1), respond: respond}
}

func TestSudoShimStreamsTheCommandAndExitsWithItsStatus(t *testing.T) {
	runner := newFakeSudo(func(_ context.Context, stdout, stderr io.Writer) (int, error) {
		if _, err := io.WriteString(stdout, "installed jq\n"); err != nil {
			return 0, err
		}
		_, err := io.WriteString(stderr, "a warning\n")
		return 3, err
	})
	socket := sudoSocket(t, runner)
	var out, errOut bytes.Buffer
	code := runSudo(t.Context(), socket, []string{"-E", "tee", "/etc/apt/sources.list.d/x.list"}, strings.NewReader("deb http://example.com stable main\n"), &out, &errOut)
	if code != 3 || out.String() != "installed jq\n" || errOut.String() != "a warning\n" {
		t.Fatalf("sudo = %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if p := <-runner.params; !slices.Equal(p.Argv, []string{"tee", "/etc/apt/sources.list.d/x.list"}) || p.User != "" || p.Dir != cwd {
		t.Fatalf("params = %+v", p)
	}
	if stdin := <-runner.stdin; string(stdin) != "deb http://example.com stable main\n" {
		t.Fatalf("stdin = %q", stdin)
	}
}

func TestSudoShimPrintsTheRefusal(t *testing.T) {
	runner := newFakeSudo(func(context.Context, io.Writer, io.Writer) (int, error) {
		return 0, &protocol.Error{Code: protocol.CodeDenied, Message: "Ada denied this command"}
	})
	socket := sudoSocket(t, runner)
	var errOut bytes.Buffer
	if code := runSudo(t.Context(), socket, []string{"rm", "-rf", "/var/lib/apt"}, nil, io.Discard, &errOut); code != 1 || errOut.String() != "sudo: Ada denied this command\n" {
		t.Fatalf("sudo = %d, stderr %q", code, errOut.String())
	}
	if stdin := <-runner.stdin; len(stdin) != 0 {
		t.Fatalf("a terminal's stdin sent %q", stdin)
	}
	errOut.Reset()
	if code := runSudo(t.Context(), socket, []string{"-v"}, nil, io.Discard, &errOut); code != 1 || errOut.String() != "sudo: -v is not supported in an Aether Default session\n" {
		t.Fatalf("sudo -v = %d, stderr %q", code, errOut.String())
	}
}

func TestSudoEndsWhenTheShimHangsUp(t *testing.T) {
	runner := newFakeSudo(func(ctx context.Context, _, _ io.Writer) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	socket := sudoSocket(t, runner)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(protocol.RunSudoParams{Argv: []string{"sleep", "600"}, Dir: "/"})
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: protocol.MethodRunSudo, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	<-runner.params
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-runner.ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("command ended with %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the command outlived the shim's connection")
	}
}
