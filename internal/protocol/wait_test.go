package protocol

import (
	"strings"
	"testing"
)

func TestParseWaitSpec(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]WaitSpec{
		"pr:acme/app#7:merged":                    {Spec: "pr:acme/app#7:merged", Kind: WaitKindPRMerged, Repo: "acme/app", Number: 7},
		"pr:Acme/My.App_2#123:checks":             {Spec: "pr:acme/my.app_2#123:checks", Kind: WaitKindPRChecks, Repo: "acme/my.app_2", Number: 123},
		"run:run-7k2m9q4xbd:finished":             {Spec: "run:run-7k2m9q4xbd:finished", Kind: WaitKindRunFinished, RunID: "run-7k2m9q4xbd"},
		"run:01hzx3k9v8q2w5e7r4t6y1u0aa:finished": {Spec: "run:01hzx3k9v8q2w5e7r4t6y1u0aa:finished", Kind: WaitKindRunFinished, RunID: "01hzx3k9v8q2w5e7r4t6y1u0aa"},
	} {
		got, err := ParseWaitSpec(raw)
		if err != nil || got != want {
			t.Errorf("ParseWaitSpec(%q) = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "pr:acme/app#7", "pr:acme/app#07:merged", "pr:acme/app#0:merged", "pr:acme#7:merged", "PR:acme/app#7:merged",
		"pr:acme/app#7:Merged", "pr:acme/app#7:closed", "run:run-x:done", "run:Run-X:finished", " run:run-x:finished",
		"pr:acme/app#7:merged\n", "https://github.com/acme/app/pull/7", "ci finishes",
		"pr:acme/app#7:checks@abc12", "pr:acme/app#7:checks@ABC1234", "pr:acme/app#7:merged@abc1234",
		"pr:-acme/app#7:merged", "pr:acme-/app#7:merged", "pr:acme/.#7:merged", "pr:acme/..#7:merged",
		"pr:" + strings.Repeat("a", 40) + "/app#7:merged", "pr:acme/" + strings.Repeat("a", 101) + "#7:merged",
	} {
		_, err := ParseWaitSpec(raw)
		if err == nil || !strings.Contains(err.Error(), WaitGrammar) || !strings.Contains(err.Error(), "until ") {
			t.Errorf("ParseWaitSpec(%q) error = %v, want one naming it and the grammar", raw, err)
		}
	}
}
