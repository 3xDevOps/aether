package ptyhost

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestSessionKeyConstructorsAndRun(t *testing.T) {
	tests := []struct {
		name string
		key  SessionKey
		want string
		run  domain.RunID
		ok   bool
	}{
		{name: "run", key: RunSession("r1"), want: "r1", run: "r1", ok: true},
		{name: "terminal", key: TerminalSession("m1", "main"), want: "terminal:m1:main", run: "", ok: false},
		{name: "run shell", key: RunShellSession("r1", "t1"), want: "run-shell:r1:t1", run: "", ok: false},
		{name: "terminal prefix", key: SessionKey("terminal:r1"), want: "terminal:r1", run: "", ok: false},
		{name: "run shell prefix", key: SessionKey("run-shell:r1"), want: "run-shell:r1", run: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.key) != tt.want {
				t.Fatalf("key = %q, want %q", tt.key, tt.want)
			}
			got, ok := tt.key.Run()
			if got != tt.run || ok != tt.ok {
				t.Fatalf("Run() = (%q, %v), want (%q, %v)", got, ok, tt.run, tt.ok)
			}
		})
	}
}
