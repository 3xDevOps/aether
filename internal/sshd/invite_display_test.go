package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

func TestInviteJoinSanitizesDisplayName(t *testing.T) {
	srv, _ := newAgentTestServer(t)
	srv.cfg.InvitesDir = t.TempDir()
	ctx := context.Background()
	code, _, err := mintInvite(srv.cfg.InvitesDir, time.Hour, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	keyLine := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))

	hostile := "\x1b[2J\x1b]0;spoof\x07" + strings.Repeat("E", maxDisplayNameRunes+50)
	m, err := srv.joinInviteMember(ctx, code, keyLine, hostile)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if strings.IndexFunc(m.DisplayName, unicode.IsControl) >= 0 {
		t.Fatalf("stored display name keeps control characters: %q", m.DisplayName)
	}
	if utf8.RuneCountInString(m.DisplayName) > maxDisplayNameRunes {
		t.Fatalf("stored display name over %d runes: %q", maxDisplayNameRunes, m.DisplayName)
	}
	if _, err := cleanDisplayName(m.DisplayName); err != nil {
		t.Fatalf("stored display name fails cleanDisplayName: %v", err)
	}
}
