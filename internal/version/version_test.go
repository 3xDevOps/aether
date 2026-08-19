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
