package runtime

import (
	"strings"
	"testing"
)

func TestLocalOnlyImage(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"aether/ws-a1b2c3:1", true},
		{"aether/ws-a1b2c3:42", true},
		{"ubuntu:24.04", false},
		{"busybox:1.36", false},
		// A registry-qualified reference is not a local Aether tag even
		// when a path component is named "aether".
		{"ghcr.io/aether/ws-a1b2c3:1", false},
		{"aether-tools:latest", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := localOnlyImage(tt.ref); got != tt.want {
			t.Errorf("localOnlyImage(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestPullRefusesLocalOnlyTag(t *testing.T) {
	// The nil client proves the daemon is never contacted: any pull
	// attempt would panic instead of returning.
	d := &Docker{}
	err := d.pull(t.Context(), "aether/ws-a1b2c3:7")
	if err == nil {
		t.Fatal("pull(local-only tag) = nil error, want error")
	}
	if !strings.Contains(err.Error(), "aether/ws-a1b2c3:7") {
		t.Errorf("pull error = %q, want the missing tag named", err)
	}
	if !strings.Contains(err.Error(), "aether env rebuild") {
		t.Errorf("pull error = %q, want it to name aether env rebuild", err)
	}
}
