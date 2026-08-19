package sshd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestLoadOrCreateHostKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh", "host_ed25519_key")
	signer, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := signer.PublicKey().Type(); got != "ssh-ed25519" {
		t.Fatalf("key type = %q, want ssh-ed25519", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file perm = %o, want 600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("key dir perm = %o, want 700", perm)
	}

	again, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !bytes.Equal(again.PublicKey().Marshal(), signer.PublicKey().Marshal()) {
		t.Error("reloaded host key differs from generated key")
	}
}

func TestLoadOrCreateHostKeyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host_key")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateHostKey(path); err == nil {
		t.Fatal("expected error for corrupt host key")
	}
}

func TestParseGitCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		op, wsID string
		ok       bool
	}{
		{"git-upload-pack 'ws_1.git'", "upload-pack", "ws_1", true},
		{"git-upload-pack /ws_1.git", "upload-pack", "ws_1", true},
		{"git-upload-pack ws_1", "upload-pack", "ws_1", true},
		{"git-receive-pack '/ws_1.git'", "receive-pack", "ws_1", true},
		{"git upload-pack 'ws_1.git'", "upload-pack", "ws_1", true},
		{"git receive-pack ws_1", "receive-pack", "ws_1", true},
		{"git-upload-pack ''", "", "", false},
		{"git-upload-pack 'a/b'", "", "", false},
		{"git-upload-pack", "", "", false},
		{"git-upload-pack a b", "", "", false},
		{"ls -la", "", "", false},
		{"git status ws", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		op, wsID, ok := parseGitCommand(tt.cmd)
		if op != tt.op || wsID != tt.wsID || ok != tt.ok {
			t.Errorf("parseGitCommand(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.cmd, op, wsID, ok, tt.op, tt.wsID, tt.ok)
		}
	}
}

func TestRPCErrorMapping(t *testing.T) {
	tests := []struct {
		err  error
		code int
	}{
		{store.ErrNotFound, protocol.CodeNotFound},
		{fmt.Errorf("get run: %w", store.ErrNotFound), protocol.CodeNotFound},
		{store.ErrConflict, protocol.CodeConflict},
		{store.ErrInUse, protocol.CodeConflict},
		{errInvalidTransition, protocol.CodeInvalidState},
		{errWriteDenied, protocol.CodeDenied},
		{errMemberRemoved, protocol.CodeDenied},
		{errMemberPending, protocol.CodeDenied},
		{errNoSession, protocol.CodeUnavailable},
		{errSessionEnded, protocol.CodeUnavailable},
		{errors.New("boom"), protocol.CodeInternal},
	}
	for _, tt := range tests {
		if got := rpcError(tt.err); got.Code != tt.code {
			t.Errorf("rpcError(%v).Code = %d, want %d", tt.err, got.Code, tt.code)
		}
	}
}
