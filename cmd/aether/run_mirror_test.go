package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestLaunchParamsCachedBaseForwarding(t *testing.T) {
	params := launchParams("ws_1", "fix it", "claude", "headless", "opus", "high", "mem_2", "0123456789abcdef0123456789abcdef01234567")
	if params.CachedBase != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("cached base = %q, want explicit SHA", params.CachedBase)
	}
	if params.WorkspaceID != "ws_1" || params.Task != "fix it" || params.Harness != "claude" ||
		params.Mode != "headless" || params.Model != "opus" || params.Effort != "high" || params.AccountMemberID != "mem_2" {
		t.Fatalf("launch params = %+v, want all direct-run fields forwarded", params)
	}
}

func TestLaunchParamsOmitEmptyCachedBase(t *testing.T) {
	params := launchParams("ws_1", "fix it", "claude", "tui", "", "", "", "")
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cached_base") {
		t.Fatalf("empty cached base was sent: %s", raw)
	}
}

func TestRunTemplateRejectsCachedBase(t *testing.T) {
	err := runRun([]string{"--template", "nightly", "--cached-base", "0123456789abcdef0123456789abcdef01234567"})
	if err == nil || !strings.Contains(err.Error(), "--cached-base") {
		t.Fatalf("run --template --cached-base error = %v, want an explicit combination refusal", err)
	}
}

func TestCachedBaseLaunchErrorPrintsExactRetry(t *testing.T) {
	const accepted = "0123456789abcdef0123456789abcdef01234567"
	data, err := json.Marshal(protocol.MirrorFailure{AcceptedCommit: accepted})
	if err != nil {
		t.Fatal(err)
	}
	params := launchParams("ws 1", "-fix API's cache", "claude", "headless", "opus", "high", "mem_2", "")
	params.FallbackAccountMemberIDs = []string{"mem_3", "mem_4"}
	got := cachedBaseLaunchError(&protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: "base capture failed: mirror offline",
		Data:    data,
	}, params, false)
	want := "base capture failed: mirror offline\nretry from cached " + accepted + " with:\n" +
		"  aether session --agent claude --mode background --model opus --effort high --workspace 'ws 1' --account mem_2 --fallback mem_3,mem_4 --cached-base " + accepted + " -- " +
		`'-fix API'\''s cache'`
	if got == nil || got.Error() != want {
		t.Fatalf("retry error = %v, want %q", got, want)
	}
	params.FallbackAccountMemberIDs, params.NoFallback = nil, true
	if got := cachedBaseLaunchError(&protocol.Error{Message: "offline", Data: data}, params, false); got == nil ||
		!strings.Contains(got.Error(), " --account mem_2 --no-fallback --cached-base ") {
		t.Fatalf("retry error = %v, want --no-fallback carried", got)
	}
}

func TestCachedBaseLaunchErrorLeavesUnstructuredErrorUntouched(t *testing.T) {
	original := &protocol.Error{Code: protocol.CodeUnavailable, Message: "base capture failed"}
	if got := cachedBaseLaunchError(original, launchParams("ws_1", "task", "claude", "tui", "", "", "", ""), false); got != original {
		t.Fatalf("error without accepted commit = %v, want original error", got)
	}

	data, err := json.Marshal(protocol.MirrorFailure{ObservedCommit: "fedcba9876543210fedcba9876543210fedcba98"})
	if err != nil {
		t.Fatal(err)
	}
	original.Data = data
	if got := cachedBaseLaunchError(original, launchParams("ws_1", "task", "claude", "tui", "", "", "", ""), false); got != original {
		t.Fatalf("failure without accepted commit = %v, want original error", got)
	}
}

func TestFormatTemplateBaseProvenance(t *testing.T) {
	got := formatTemplateBase(protocol.TemplateLaunchResult{
		BaseBranch:    "main",
		BaseCommit:    "0123456789abcdef0123456789abcdef01234567",
		BaseSource:    "github.com/acme/app",
		BaseCheckedAt: "2026-09-11T12:34:56Z",
	})
	want := "base main 0123456789abcdef0123456789abcdef01234567 from github.com/acme/app (checked 2026-09-11T12:34:56Z)\n"
	if got != want {
		t.Fatalf("template base = %q, want %q", got, want)
	}

	local := formatTemplateBase(protocol.TemplateLaunchResult{BaseBranch: "main", BaseCommit: "abc", BaseCheckedAt: "now"})
	if want := "base main abc from local (checked now)\n"; local != want {
		t.Fatalf("local template base = %q, want %q", local, want)
	}
}

// A launch call with no answer from the server may still be starting, so
// the error names the retry that returns its run instead of a second one.
func TestLaunchErrorNamesTheRetryWithTheKey(t *testing.T) {
	params := launchParams("ws_1", "fix it", "claude", "tui", "", "", "", "")
	params.IdempotencyKey = "k-1"
	err := launchError(errors.New("protocol: read response: EOF"), params, false)
	want := "protocol: read response: EOF\nthe launch may still be starting; aether sessions lists it. To retry without launching twice, run:\n" +
		"  aether session --agent claude --mode standard --workspace ws_1 --idempotency-key k-1 -- 'fix it'"
	if err == nil || err.Error() != want {
		t.Fatalf("launch error = %v\nwant %q", err, want)
	}
	err = launchError(errInterrupted, params, true)
	var status *exitStatusError
	if !errors.As(err, &status) || status.code != 130 || !errors.Is(err, errInterrupted) ||
		!strings.Contains(err.Error(), "the launch continues on the server") || !strings.HasSuffix(err.Error(), "--idempotency-key k-1 -- - < <the same task file>") {
		t.Fatalf("interrupted launch error = %v", err)
	}
	refused := &protocol.Error{Code: protocol.CodeUnavailable, Message: "scheduler: host capacity unavailable"}
	if got := launchError(refused, params, false); got != refused {
		t.Fatalf("server refusal = %v, want it unchanged", got)
	}
}
