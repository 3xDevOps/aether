package sshd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMintBurnInvite(t *testing.T) {
	dir := t.TempDir()
	code, expires, err := mintInvite(dir, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !isInviteCode(code) {
		t.Fatalf("code %q is not hex", code)
	}
	if !inviteUsable(dir, code) {
		t.Fatal("fresh invite not usable")
	}
	if expires.Before(time.Now()) {
		t.Fatal("expiry is in the past")
	}
	if _, err := os.Stat(filepath.Join(dir, code)); err != nil {
		t.Fatalf("invite file: %v", err)
	}
	if err := burnInvite(dir, code); err != nil {
		t.Fatalf("burn: %v", err)
	}
	if inviteUsable(dir, code) {
		t.Fatal("burned invite still usable")
	}
}

func TestInviteExpiry(t *testing.T) {
	dir := t.TempDir()
	code, _, err := mintInvite(dir, time.Millisecond)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for inviteUsable(dir, code) {
		if time.Now().After(deadline) {
			t.Fatal("expired invite still usable")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestParseInviteUser(t *testing.T) {
	tests := []struct {
		user, code, display string
		ok                  bool
	}{
		{"invite:deadbeef", "deadbeef", "", true},
		{"invite:abc123:Ada", "abc123", "Ada", true},
		{"invite:ABC", "ABC", "", true},
		{"aether", "", "", false},
		{"invite:", "", "", false},
		{"invite:not-hex", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		code, display, ok := parseInviteUser(tt.user)
		if code != tt.code || display != tt.display || ok != tt.ok {
			t.Errorf("parseInviteUser(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.user, code, display, ok, tt.code, tt.display, tt.ok)
		}
	}
}
