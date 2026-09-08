package main

import "testing"

func TestParseWorkspaceOriginArgs(t *testing.T) {
	const url = "https://github.com/acme/app.git"
	cases := []struct {
		name string
		args []string
		want originChange
	}{
		{"show", nil, originChange{}},
		{"show a named workspace", []string{"--workspace", "app"}, originChange{workspace: "app"}},
		{"set", []string{url}, originChange{origin: url, set: true}},
		{"set a named workspace", []string{"--workspace", "app", url}, originChange{workspace: "app", origin: url, set: true}},
		{"clear", []string{"--clear"}, originChange{set: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWorkspaceOriginArgs(tc.args)
			if err != nil || got != tc.want {
				t.Fatalf("parseWorkspaceOriginArgs(%v) = (%+v, %v), want (%+v, nil)", tc.args, got, err, tc.want)
			}
		})
	}

	// A URL and --clear together say two different things; so does a
	// second positional argument.
	for _, args := range [][]string{{url, "--clear"}, {"--clear", url}, {url, url}} {
		if _, err := parseWorkspaceOriginArgs(args); err == nil {
			t.Errorf("parseWorkspaceOriginArgs(%v) accepted a contradictory command line", args)
		}
	}
}
