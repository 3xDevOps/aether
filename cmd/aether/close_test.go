package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// --force closes even when the server cannot say what the close affects.
func TestCloseSessionForceWithoutCheck(t *testing.T) {
	closeReply := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "run-1", Status: "abandoned"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunClose: closeReply})
	var out bytes.Buffer
	if err := closeSession(c, "run-1", "abandoned", false, false, strings.NewReader(""), &out); err == nil || !strings.Contains(err.Error(), "the server is older than this CLI") {
		t.Fatalf("closeSession against an older server = %v, want the update hint", err)
	}
	if closeReply.params() != nil {
		t.Fatal("closed without --force though the check failed")
	}
	if err := closeSession(c, "run-1", "abandoned", true, false, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Could not check what closing run-1 affects: ") || closeReply.params() == nil {
		t.Fatalf("closeSession --force printed %q, closed %v", out.String(), closeReply.params() != nil)
	}
}

func TestCloseSessionWarns(t *testing.T) {
	warned := protocol.RunCloseCheckResult{Runs: []protocol.RunCloseCheck{{RunID: "run-1", Warnings: []protocol.CloseWarning{
		{Kind: protocol.CloseWarningPullRequest, Detail: "Pull request acme/api#7 is open, not merged", URL: "https://github.com/acme/api/pull/7"}}}}}
	for _, tc := range []struct {
		name               string
		force, interactive bool
		answer             string
		closed             bool
		wantErr            string
	}{
		{name: "no terminal", wantErr: "no terminal to ask on; to close anyway: aether close run-1 --outcome merged --force"},
		{name: "force", force: true, closed: true},
		{name: "declined", interactive: true, answer: "n\n", wantErr: "aether close: run-1 was not closed"},
		{name: "confirmed", interactive: true, answer: "y\n", closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closeReply := &fakeReply{result: protocol.RunResult{Run: protocol.Run{ID: "run-1", Status: "merged"}}}
			c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunCloseCheck: {result: warned}, protocol.MethodRunClose: closeReply})
			var out bytes.Buffer
			err := closeSession(c, "run-1", "merged", tc.force, tc.interactive, strings.NewReader(tc.answer), &out)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr) {
				t.Fatalf("closeSession = %v, want %q", err, tc.wantErr)
			}
			if !strings.Contains(out.String(), "pull_request: Pull request acme/api#7 is open, not merged\n    https://github.com/acme/api/pull/7\n") {
				t.Errorf("printed %q, want the warning and its link", out.String())
			}
			if closed := closeReply.params() != nil; closed != tc.closed {
				t.Errorf("run.close called = %v, want %v", closed, tc.closed)
			}
		})
	}
}
