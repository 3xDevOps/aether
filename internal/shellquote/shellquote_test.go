package shellquote

import "testing"

func TestQuote(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"skills/deploy/README.md", "skills/deploy/README.md"},
		{"a-b_c@d%e+f=g:h,i.j", "a-b_c@d%e+f=g:h,i.j"},
		{"", "''"},
		{"skills/deploy notes/README.md", "'skills/deploy notes/README.md'"},
		{"skills/o'brien/README.md", `'skills/o'\''brien/README.md'`},
		{"a;rm -rf b", "'a;rm -rf b'"},
		{"$HOME/x", "'$HOME/x'"},
		{"notes/*.md", "'notes/*.md'"},
	} {
		if got := Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
