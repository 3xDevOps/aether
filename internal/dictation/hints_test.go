package dictation

import "testing"

func TestApplyHints(t *testing.T) {
	hints := []string{"aether", "loginMounts", "aether-core", "syncd", "run-ddm0m1xy2f", "PR"}
	for _, tc := range []struct{ in, want string }{
		{"open sync d now", "open syncd now"},
		{"check login mounts.", "check loginMounts."},
		{"the Aether core repo", "the aether-core repo"},
		{"Aether, please", "aether, please"},
		{"run ddm0m1xy2f finished", "run-ddm0m1xy2f finished"},
		{"(login mounts)", "(loginMounts)"},
		{"sync, d", "sync, d"},
		{"login mount", "login mount"},
		{"ether", "ether"},
		{"syncd's log", "syncd's log"},
		{"open a pr", "open a PR"},
		{"  spaced   out  ", "spaced out"},
		{"", ""},
	} {
		if got := ApplyHints(tc.in, hints); got != tc.want {
			t.Errorf("ApplyHints(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
