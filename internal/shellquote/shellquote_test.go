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

func TestPath(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"skills/deploy/README.md", "skills/deploy/README.md"},
		{"-x.md", "./-x.md"},
		{"--allow-secret", "./--allow-secret"},
		{"-a b.md", "'./-a b.md'"},
		// Only a leading dash reads as a flag; one inside a path does not.
		{"skills/my-skill/README.md", "skills/my-skill/README.md"},
	} {
		if got := Path(tc.in); got != tc.want {
			t.Errorf("Path(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A generated script quotes every value it carries, so QuoteAlways holds
// even for a string Quote would have left bare.
func TestQuoteAlways(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"/usr/local/bin/aether", "'/usr/local/bin/aether'"},
		{"", "''"},
		{`/tmp/it's odd`, `'/tmp/it'\''s odd'`},
	} {
		if got := QuoteAlways(tc.in); got != tc.want {
			t.Errorf("QuoteAlways(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
