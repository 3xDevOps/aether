package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCloseSessionWarns(t *testing.T) {
	warned := protocol.RunCloseCheckResult{Runs: []protocol.RunCloseCheck{{RunID: "run-1", Warnings: []protocol.CloseWarning{
		{Kind: protocol.CloseWarningPullRequest, Detail: "Pull request acme/api#7 was open, not merged", URL: "https://github.com/acme/api/pull/7"}}}}}
	for _, tc := range []struct {
		name               string
		force, interactive bool
		answer             string
		closed             bool
		wantErr            string
	}{
		{name: "no terminal", closed: true},
		{name: "force", force: true, interactive: true, closed: true},
		{name: "declined", interactive: true, answer: "n\n", wantErr: "aether close: run-1 was not closed"},
		{name: "confirmed", interactive: true, answer: "y\n", closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := &fakeReply{result: warned}
			closeReply := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "run-1", Status: "merged"}}}
			c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunCloseCheck: check, protocol.MethodRunClose: closeReply})
			var out, errOut bytes.Buffer
			err := closeSession(c, "run-1", "merged", tc.force, tc.interactive, strings.NewReader(tc.answer), &out, &errOut)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr) {
				t.Fatalf("closeSession = %v, want %q", err, tc.wantErr)
			}
			if !strings.Contains(errOut.String(), "pull_request: Pull request acme/api#7 was open, not merged\n    https://github.com/acme/api/pull/7\n") {
				t.Errorf("stderr = %q, want the warning and its link", errOut.String())
			}
			if !strings.Contains(string(check.params()), `"outcome":"merged"`) {
				t.Errorf("run.close_check params = %s, want the outcome", check.params())
			}
			if closed := closeReply.params() != nil; closed != tc.closed {
				t.Errorf("run.close called = %v, want %v", closed, tc.closed)
			}
			if tc.closed && out.String() != "session run-1 merged\n" {
				t.Errorf("stdout = %q, want only the result", out.String())
			}
		})
	}
}

// A server that cannot run the check, such as one older than the CLI,
// still closes, saying why nothing was checked.
func TestCloseSessionWithoutCheck(t *testing.T) {
	closeReply := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "run-1", Status: "abandoned"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunClose: closeReply})
	var out, errOut bytes.Buffer
	if err := closeSession(c, "run-1", "abandoned", false, true, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "Could not check what closing run-1 affects: ") || !strings.Contains(errOut.String(), "the server is older than this CLI") || closeReply.params() == nil {
		t.Fatalf("stderr %q, closed %v", errOut.String(), closeReply.params() != nil)
	}
}
