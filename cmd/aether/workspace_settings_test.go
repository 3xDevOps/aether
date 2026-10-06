package main

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestParseMessageOthers(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"everyone":                   "",
		"admins-only":                domain.SteerOthersAdminsOnly,
		domain.SteerOthersAdminsOnly: domain.SteerOthersAdminsOnly,
	} {
		got, err := parseMessageOthers(in)
		if err != nil || got != want {
			t.Errorf("parseMessageOthers(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	if _, err := parseMessageOthers("nobody"); err == nil {
		t.Error("parseMessageOthers(nobody) accepted an undefined policy")
	}
}

// Every wire value reads back as a spelling the flag accepts, so the
// output of `workspace settings` can be typed straight back in.
func TestDescribeMessageOthersRoundTrips(t *testing.T) {
	for _, wire := range []string{"", domain.SteerOthersAdminsOnly} {
		desc := describeMessageOthers(wire)
		word := desc[:len(desc)-len(" (")-len(desc[indexOf(desc, " (")+2:])]
		if got, err := parseMessageOthers(word); err != nil || got != wire {
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
