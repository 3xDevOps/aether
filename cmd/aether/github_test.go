package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestGitHubUsage(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"login"}, {"connect", "extra"}} {
		if err := runGitHub(args); err == nil || err.Error() != githubUsage {
			t.Errorf("runGitHub(%v) = %v, want %q", args, err, githubUsage)
		}
	}
}

// replySequence answers each request with the next value, repeating the last.
type replySequence []any

func (s *replySequence) MarshalJSON() ([]byte, error) {
	next := (*s)[0]
	if len(*s) > 1 {
		*s = (*s)[1:]
	}
	return json.Marshal(next)
}

func TestGitHubConnect(t *testing.T) {
	githubPollInterval = time.Millisecond
	notLoggedIn := &protocol.Error{Code: protocol.CodeInvalidState, Message: "github: not logged in to github.com in the environment terminal; run aether github connect: not logged in"}
	pending := protocol.GitHubOAuthResult{State: "pending", SessionID: "s1", UserCode: "ABCD-1234",
		VerificationURL: "https://github.com/login/device", ExpiresAt: "2026-10-25T06:30:00Z"}
	connected := protocol.GitHubOAuthResult{State: "connected", SessionID: "s1", Login: "octocat",
		Connection: &protocol.GitHubConnectResult{Login: "octocat", Fingerprint: "SHA256:abc"}}
	const deviceOutput = "url: https://github.com/login/device\ncode: ABCD-1234\n" +
		"scopes: repo, read:org, gist, admin:ssh_signing_key\nexpires: 2026-10-25T06:30:00Z\n" +
		"open the URL in your browser and enter the code; waiting for approval...\n"
	const connectedOutput = "logged in to github.com as octocat\nsigning key SHA256:abc registered on GitHub\n"
	for _, tc := range []struct {
		name    string
		replies map[string]*fakeReply
		out     string
		err     string
	}{
		{
			name:    "existing login",
			replies: map[string]*fakeReply{protocol.MethodGitHubConnect: {result: protocol.GitHubConnectResult{Login: "octocat", Fingerprint: "SHA256:abc"}}},
			out:     connectedOutput,
		},
		{
			name: "device flow",
			replies: map[string]*fakeReply{
				protocol.MethodGitHubConnect:     {err: notLoggedIn},
				protocol.MethodGitHubOAuthStart:  {result: protocol.GitHubOAuthResult{State: "starting", SessionID: "s1"}},
				protocol.MethodGitHubOAuthStatus: {result: &replySequence{pending, pending, protocol.GitHubOAuthResult{State: "finishing", SessionID: "s1"}, connected}},
			},
			out: deviceOutput + connectedOutput,
		},
		{
			name: "expired code",
			replies: map[string]*fakeReply{
				protocol.MethodGitHubConnect:     {err: notLoggedIn},
				protocol.MethodGitHubOAuthStart:  {result: pending},
				protocol.MethodGitHubOAuthStatus: {result: protocol.GitHubOAuthResult{State: "expired", SessionID: "s1", Error: "github: authorization expired after 15 minutes; connect again"}},
			},
			out: deviceOutput,
			err: "github: authorization expired after 15 minutes; connect again\nrun aether github connect for a new code",
		},
		{
			name: "not an admin",
			replies: map[string]*fakeReply{
				protocol.MethodGitHubConnect:    {err: notLoggedIn},
				protocol.MethodGitHubOAuthStart: {err: &protocol.Error{Code: protocol.CodeDenied, Message: "github.oauth.start requires the admin role"}},
			},
			err: "run this in aether terminal, then aether github connect again:\n  " + githubLoginCommand,
		},
		{
			name: "other refusal",
			replies: map[string]*fakeReply{protocol.MethodGitHubConnect: {err: &protocol.Error{Code: protocol.CodeInvalidState,
				Message: "github: gh is not on PATH in the environment terminal"}}},
			err: "github: gh is not on PATH in the environment terminal",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := githubConnect(fakeControlMethods(t, tc.replies), &out)
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("githubConnect error = %v, want %q", err, tc.err)
			}
			if out.String() != tc.out {
				t.Fatalf("output =\n%s\nwant\n%s", out.String(), tc.out)
			}
			if status := tc.replies[protocol.MethodGitHubOAuthStatus]; status != nil && string(status.got) != `{"session_id":"s1"}` {
				t.Fatalf("status params = %s, want the started session", status.got)
			}
		})
	}
}

func TestGitHubConnectInterruptedWhileTheServerIsSilent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot send itself os.Interrupt on Windows")
	}
	githubPollInterval = time.Millisecond
	githubCancelTimeout = 50 * time.Millisecond
	server, client := net.Pipe()
	go func() {
		defer server.Close() //nolint:errcheck
		r := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var req protocol.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}
			if req.Method == protocol.MethodGitHubOAuthStart {
				result, _ := json.Marshal(protocol.GitHubOAuthResult{State: "starting", SessionID: "s1"})
				out, _ := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: result})
				if _, err := server.Write(append(out, '\n')); err != nil {
					return
				}
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })

	guard := make(chan os.Signal, 1)
	signal.Notify(guard, os.Interrupt)
	defer signal.Stop(guard)
	done := make(chan error, 1)
	go func() { done <- githubDeviceConnect(c, &bytes.Buffer{}) }()
	for {
		select {
		case err := <-done:
			var status *exitStatusError
			if !errors.As(err, &status) || status.code != 130 || !errors.Is(err, errInterrupted) ||
				!strings.Contains(err.Error(), "may still be pending and expires on its own") {
				t.Fatalf("githubDeviceConnect = %v, want interrupted status 130 naming the pending sign-in", err)
			}
			return
		case <-time.After(20 * time.Millisecond):
			self, _ := os.FindProcess(os.Getpid())
			_ = self.Signal(os.Interrupt)
		}
	}
}
