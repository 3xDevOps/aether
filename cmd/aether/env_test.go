package main

import (
	"strings"
	"testing"
)

func TestEnvResetRefusesBeforeConnectingWithoutConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interactive bool
		answer      string
		want        string
	}{
		{name: "no terminal", want: "--yes is required"},
		{name: "answered no", interactive: true, answer: "n\n", want: "nothing was reset"},
		{name: "no answer", interactive: true, want: "nothing was reset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			err := envReset(false, tc.interactive, strings.NewReader(tc.answer), &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("envReset = %v, want %q", err, tc.want)
			}
			if tc.interactive != strings.Contains(out.String(), "[y/N]") {
				t.Fatalf("prompt shown = %q, want it only on a terminal", out.String())
			}
		})
	}
}
