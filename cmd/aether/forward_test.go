package main

import "testing"

func TestParseForwardArgs(t *testing.T) {
	tests := []struct {
		name                string
		args                []string
		wantTarget          string
		wantPort, wantLocal int
		wantErr             bool
	}{
		{name: "default local", args: []string{"run_1", "1455"}, wantTarget: "run:run_1", wantPort: 1455, wantLocal: 1455},
		{name: "terminal", args: []string{"terminal", "1455"}, wantTarget: "terminal", wantPort: 1455, wantLocal: 1455},
		{name: "local after positionals", args: []string{"run_1", "1455", "--local", "2455"}, wantTarget: "run:run_1", wantPort: 1455, wantLocal: 2455},
		{name: "local before positionals", args: []string{"--local", "2455", "run_1", "1455"}, wantTarget: "run:run_1", wantPort: 1455, wantLocal: 2455},
		{name: "bad port", args: []string{"run_1", "0"}, wantErr: true},
		{name: "missing run", args: []string{"1455"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, port, local, err := parseForwardArgs(test.args)
			if test.wantErr {
				if err == nil {
					t.Fatal("parseForwardArgs succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if target != test.wantTarget || port != test.wantPort || local != test.wantLocal {
				t.Fatalf("got %q %d %d, want %q %d %d", target, port, local, test.wantTarget, test.wantPort, test.wantLocal)
			}
		})
	}
}
