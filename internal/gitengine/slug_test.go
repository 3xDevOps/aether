package gitengine

import "testing"

func TestSlugify(t *testing.T) {
	cases := []struct{ task, want string }{
		{"fix the auth bug", "fix-the-auth-bug"},
		{"Fix THE Auth Bug!", "fix-the-auth-bug"},
		{"  --weird__ / stuff  ", "weird-stuff"},
		{"añadir función", "a-adir-funci-n"},
		{"", ""},
		{"!!!", ""},
		{"---", ""},
		{"a", "a"},
		{"UPPER123lower", "upper123lower"},
		{"refactor the entire authentication subsystem", "refactor-the-entire-authenticati"},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbb", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for _, c := range cases {
		if got := slugify(c.task); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.task, got, c.want)
		}
		if len(slugify(c.task)) > maxSlugLen {
			t.Errorf("slugify(%q) exceeds %d chars", c.task, maxSlugLen)
		}
	}
}

// The branch name is what a member reads in `git branch` and types into
// `git diff`, so the task leads and the ID is a short tail rather than a
// 26-character prefix nobody can retype.
func TestRunBranchLeadsWithTheTask(t *testing.T) {
	const run = "01m0h6tym4y65102a721nq0jf3"
	got := runBranch(run, "fix the bug", shortID(run))
	if want := "aether/run-fix-the-bug-nq0jf3"; got != want {
		t.Errorf("runBranch = %q, want %q", got, want)
	}
	if len(got) > 40 {
		t.Errorf("runBranch = %q, %d chars: too long to read or retype", got, len(got))
	}
}

func TestRunBranchWithoutAUsableTask(t *testing.T) {
	const run = "01m0h6tym4y65102a721nq0jf3"
	if got := runBranch(run, "!!!", shortID(run)); got != "aether/run-nq0jf3" {
		t.Errorf("runBranch with empty slug = %q", got)
	}
}

func TestShortIDTakesTheRandomTail(t *testing.T) {
	// ULIDs are timestamp-first, so two runs created in the same
	// millisecond share their leading characters. The tail is the part
	// that actually distinguishes them.
	const a = "01m0h6tym4y65102a721nq0jf3"
	const b = "01m0h6tym4y65102a721zzzzzz"
	if shortID(a) == shortID(b) {
		t.Errorf("shortID collapsed two distinct runs to %q", shortID(a))
	}
	if got := shortID("r1"); got != "r1" {
		t.Errorf("shortID of a short id = %q, want it unchanged", got)
	}
}
