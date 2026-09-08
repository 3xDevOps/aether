package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// --key is validated and resolved to an absolute path while parsing, then
// passed to cli.Link as the key choice so saved-key inheritance stays there.
func TestParseLinkArgsKey(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("deploy_key", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	opts, err := parseLinkArgs([]string{"my-server", "--key", "deploy_key", "--name", "prod"})
	if err != nil {
		t.Fatalf("parseLinkArgs: %v", err)
	}
	want := filepath.Join(cwd, "deploy_key")
	if opts.key != want {
		t.Errorf("key = %q, want %q", opts.key, want)
	}

	// Without --key the choice stays empty, so cli.Link falls back to the
	// key a previous link saved or to ~/.ssh/id_ed25519.
	opts, err = parseLinkArgs([]string{"my-server"})
	if err != nil {
		t.Fatalf("parseLinkArgs without --key: %v", err)
	}
	if opts.key != "" {
		t.Errorf("key = %q, want empty without --key", opts.key)
	}
}

// A --key path that is not there must fail on the path the user typed,
// before the dial turns it into an authentication failure naming no file.
func TestParseLinkArgsMissingKey(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here")

	_, err := parseLinkArgs([]string{"my-server", "--key", missing})
	if err == nil {
		t.Fatal("parseLinkArgs accepted a --key path that does not exist")
	}
	for _, want := range []string{"link --key", missing} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// originStub answers workspace.origin the way the server does for a caller
// or a URL it will not take, and records what it was asked.
type originStub struct {
	err    error
	called bool
}

func (s *originStub) Call(method string, params, result any) error {
	s.called = true
	if method != protocol.MethodWorkspaceOrigin {
		return fmt.Errorf("unexpected call %s", method)
	}
	if s.err != nil {
		return s.err
	}
	res, ok := result.(*protocol.WorkspaceOriginResult)
	if !ok {
		return fmt.Errorf("unexpected result type %T", result)
	}
	res.Workspace = protocol.Workspace{
		ID:     params.(protocol.WorkspaceOriginParams).WorkspaceID,
		Origin: params.(protocol.WorkspaceOriginParams).Origin,
	}
	return nil
}

// originRepo is a scratch clone whose origin remote is upstream.
func originRepo(t *testing.T, upstream string) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", upstream}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

// Recording the clone's origin is a bonus the link never fails over. A
// viewer is denied it and a URL the server will not take is refused;
// either way the link that already wrote the remote finishes quietly.
func TestRecordWorkspaceOriginToleratesARefusal(t *testing.T) {
	const upstream = "https://github.com/acme/app.git"
	list := []protocol.Workspace{{ID: "ws_1", Name: "app"}}
	repo := originRepo(t, upstream)

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"viewer", &protocol.Error{Code: protocol.CodeDenied, Message: "pushing to a workspace requires the collaborator role"}},
		{"rejected origin", &protocol.Error{Code: protocol.CodeInvalidParams, Message: "origin must be empty or a git URL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &originStub{err: tc.err}
			out, err := captureStdout(t, func() error {
				return recordWorkspaceOrigin(stub, list, "ws_1", repo)
			})
			if err != nil {
				t.Fatalf("recordWorkspaceOrigin = %v, want the link to finish", err)
			}
			if !stub.called {
				t.Fatal("workspace.origin was never attempted")
			}
			if out != "" {
				t.Fatalf("printed %q, want nothing when the origin was not recorded", out)
			}
		})
	}

	// Any other failure is still the link's failure.
	stub := &originStub{err: &protocol.Error{Code: protocol.CodeInternal, Message: "store closed"}}
	if _, err := captureStdout(t, func() error {
		return recordWorkspaceOrigin(stub, list, "ws_1", repo)
	}); err == nil {
		t.Fatal("recordWorkspaceOrigin swallowed an internal error")
	}

	// A recorded origin is reported as the server stored it.
	out, err := captureStdout(t, func() error {
		return recordWorkspaceOrigin(&originStub{}, list, "ws_1", repo)
	})
	if err != nil {
		t.Fatalf("recordWorkspaceOrigin: %v", err)
	}
	if want := "workspace origin -> " + upstream + "\n"; out != want {
		t.Fatalf("printed %q, want %q", out, want)
	}
}
