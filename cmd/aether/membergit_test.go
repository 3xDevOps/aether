package main

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMemberGitLines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member protocol.Member
		want   []string
	}{
		{
			name:   "set",
			member: protocol.Member{ID: "mem_01abc", DisplayName: "Bob", GitName: "Ada Lovelace", GitEmail: "ada@example.com"},
			want:   []string{"git name   Ada Lovelace", "git email  ada@example.com"},
		},
		{
			name:   "unset",
			member: protocol.Member{ID: "mem_01abc", DisplayName: "Bob"},
			want: []string{
				"git name   Bob (fallback: display name)",
				"git email  mem_01abc@aether.local (fallback)",
			},
		},
		{
			name:   "half set",
			member: protocol.Member{ID: "mem_01abc", DisplayName: "Bob", GitEmail: "bob@example.com"},
			want: []string{
				"git name   Bob (fallback: display name)",
				"git email  bob@example.com",
			},
		},
	} {
		got := memberGitLines(tc.member)
		if len(got) != len(tc.want) || got[0] != tc.want[0] || got[1] != tc.want[1] {
			t.Errorf("%s: memberGitLines = %q, want %q", tc.name, got, tc.want)
		}
	}
}
