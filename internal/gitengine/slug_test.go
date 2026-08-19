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
		{"refactor the entire authentication subsystem", "refactor-the-entire-auth"},
		{"aaaaaaaaaaaaaaaaaaaaaaa bbbb", "aaaaaaaaaaaaaaaaaaaaaaa"},
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

func TestRunBranch(t *testing.T) {
	if got := runBranch("r1", "fix the bug"); got != "aether/run-r1-fix-the-bug" {
		t.Errorf("runBranch = %q", got)
	}
	if got := runBranch("r1", "!!!"); got != "aether/run-r1" {
		t.Errorf("runBranch with empty slug = %q", got)
	}
}
