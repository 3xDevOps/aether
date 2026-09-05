package main

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestParseSteerOthers(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"everyone":                   "",
		"admins-only":                domain.SteerOthersAdminsOnly,
		domain.SteerOthersAdminsOnly: domain.SteerOthersAdminsOnly,
	} {
		got, err := parseSteerOthers(in)
		if err != nil || got != want {
			t.Errorf("parseSteerOthers(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	if _, err := parseSteerOthers("nobody"); err == nil {
		t.Error("parseSteerOthers(nobody) accepted an undefined policy")
	}
}

// Every wire value reads back as a spelling the flag accepts, so the
// output of `workspace settings` can be typed straight back in.
func TestDescribeSteerOthersRoundTrips(t *testing.T) {
	for _, wire := range []string{"", domain.SteerOthersAdminsOnly} {
		desc := describeSteerOthers(wire)
		word := desc[:len(desc)-len(" (")-len(desc[indexOf(desc, " (")+2:])]
		if got, err := parseSteerOthers(word); err != nil || got != wire {
			t.Errorf("describe(%q) = %q; parse(%q) = (%q, %v), want %q", wire, desc, word, got, err, wire)
		}
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
