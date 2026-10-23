package domain

import "testing"

func TestRepoKeyRoundTripsToParentRun(t *testing.T) {
	t.Parallel()
	key := RepoKey("run-7k2m9q4xbd", 2)
	if key != "run-7k2m9q4xbd-r2" {
		t.Fatalf("key = %q", key)
	}
	if parent, extra := ParentRun(key); !extra || parent != "run-7k2m9q4xbd" {
		t.Fatalf("ParentRun(%q) = %q, %v", key, parent, extra)
	}
	for _, own := range []RunID{"run-7k2m9q4xbd", "run-r2", "01j9z8y7x6w5v4u3t2s1r0q9p8", "run-abc-rx"} {
		if parent, extra := ParentRun(own); extra || parent != own {
			t.Fatalf("ParentRun(%q) = %q, %v; a run's own id is not a key", own, parent, extra)
		}
	}
}

func TestRepoDirName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{"api": "api", "my api": "my-api", "org/lib.go": "org-lib.go", "..": "repo", "": "repo", "-x-": "x"} {
		if got := RepoDirName(name); got != want {
			t.Errorf("RepoDirName(%q) = %q, want %q", name, got, want)
		}
	}
}
