package main

import "testing"

func TestGitHubUsage(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"login"}, {"connect", "extra"}} {
		if err := runGitHub(args); err == nil || err.Error() != githubUsage {
			t.Errorf("runGitHub(%v) = %v, want %q", args, err, githubUsage)
		}
	}
}
