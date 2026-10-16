package version

import "testing"

func TestStringDefaults(t *testing.T) {
	got := String()
	want := "dev (unknown)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStringUsesLdflagsValues(t *testing.T) {
	oldVersion, oldCommit := Version, Commit
	defer func() { Version, Commit = oldVersion, oldCommit }()

	Version = "v1.2.3"
	Commit = "abc1234"
	got := String()
	want := "v1.2.3 (abc1234)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestReleaseCore(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"v0.8.0":                          "v0.8.0",
		"v0.8.0-alpha.3":                  "v0.8.0-alpha.3",
		"v0.8.0-alpha.3-5-gabc123":        "v0.8.0-alpha.3",
		"v0.8.0-alpha.3-5-gabc123-dirty":  "v0.8.0-alpha.3",
		"v0.8.0-dirty":                    "v0.8.0",
		"v0.8.0-12-g0123456789abcdef0123": "v0.8.0",
		"dev":                             "dev",
		"abc1234":                         "abc1234",
	}
	for in, want := range cases {
		if got := ReleaseCore(in); got != want {
			t.Errorf("ReleaseCore(%q) = %q, want %q", in, got, want)
		}
	}
}
