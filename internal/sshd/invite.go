package sshd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

const defaultInviteTTL = 24 * time.Hour

// inviteRecord is an open invite code. IssuedBy is the admin who minted
// it, empty for codes minted before it was recorded.
type inviteRecord struct {
	ExpiresAt time.Time       `json:"expires_at"`
	IssuedBy  domain.MemberID `json:"issued_by,omitempty"`
}

// parseInviteUser recognizes SSH usernames of the form invite:<code> or
// invite:<code>:<display>. The code must be hexadecimal.
func parseInviteUser(user string) (code, display string, ok bool) {
	rest, found := strings.CutPrefix(user, "invite:")
	if !found || rest == "" {
		return "", "", false
	}
	code, display, _ = strings.Cut(rest, ":")
	if code == "" || !isInviteCode(code) {
		return "", "", false
	}
	display = strings.TrimSpace(display)
	return code, display, true
}

func isInviteCode(code string) bool {
	if code == "" {
		return false
	}
	for _, r := range code {
		if !unicode.Is(unicode.ASCII_Hex_Digit, r) {
			return false
		}
	}
	return true
}

func invitePath(dir, code string) string {
	return filepath.Join(dir, code)
}

func mintInvite(dir string, ttl time.Duration, issuer domain.MemberID) (code string, expires time.Time, err error) {
	if dir == "" {
		return "", time.Time{}, errors.New("sshd: invites directory not configured")
	}
	if ttl <= 0 {
		ttl = defaultInviteTTL
	}
	var raw [16]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("sshd: generate invite: %w", err)
	}
	code = hex.EncodeToString(raw[:])
	expires = time.Now().UTC().Add(ttl)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", time.Time{}, fmt.Errorf("sshd: create invites dir: %w", err)
	}
	rec := inviteRecord{ExpiresAt: expires, IssuedBy: issuer}
	body, err := json.Marshal(rec)
	if err != nil {
		return "", time.Time{}, err
	}
	path := invitePath(dir, code)
	if err = os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return "", time.Time{}, fmt.Errorf("sshd: write invite: %w", err)
	}
	return code, expires, nil
}

func loadInvite(dir, code string) (inviteRecord, error) {
	var rec inviteRecord
	if dir == "" || !isInviteCode(code) {
		return rec, os.ErrNotExist
	}
	body, err := os.ReadFile(invitePath(dir, code))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		return rec, fmt.Errorf("sshd: parse invite: %w", err)
	}
	return rec, nil
}

func inviteUsable(dir, code string) bool {
	rec, err := loadInvite(dir, code)
	if err != nil {
		return false
	}
	return time.Now().Before(rec.ExpiresAt)
}

func burnInvite(dir, code string) error {
	if dir == "" || !isInviteCode(code) {
		return os.ErrNotExist
	}
	err := os.Remove(invitePath(dir, code))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return err
}

func (s *Server) inviteUsable(code string) bool {
	return inviteUsable(s.cfg.InvitesDir, code)
}

// joinInviteMember burns a validated invite and registers the proven key
// as a collaborator. Called only after the SSH handshake has verified
// possession of the private key (same deferred-write pattern as bootstrap).
func (s *Server) joinInviteMember(ctx context.Context, code, keyLine, display string) (*domain.Member, error) {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	rec, err := loadInvite(s.cfg.InvitesDir, code)
	if err != nil || !time.Now().Before(rec.ExpiresAt) {
		return nil, errors.New("sshd: invite is invalid or expired")
	}
	// A code grants what its issuer could, so it dies with the issuer's
	// admin role, through member.role or a merge that demoted them.
	if rec.IssuedBy != "" {
		admin, aerr := s.inviteIssuerAdmin(ctx, rec.IssuedBy)
		if aerr != nil {
			return nil, fmt.Errorf("sshd: invite join: %w", aerr)
		}
		if !admin {
			refused := fmt.Errorf("sshd: invite was issued by %s, who is no longer an admin", rec.IssuedBy)
			if berr := burnInvite(s.cfg.InvitesDir, code); berr != nil {
				return nil, fmt.Errorf("%w; burn invite: %w", refused, berr)
			}
			return nil, refused
		}
	}
	if err = burnInvite(s.cfg.InvitesDir, code); err != nil {
		return nil, fmt.Errorf("sshd: burn invite: %w", err)
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		_ = restoreInvite(s.cfg.InvitesDir, code, rec)
		return nil, fmt.Errorf("sshd: invite join: %w", err)
	}
	if display == "" {
		display = "collaborator"
	}
	m := &domain.Member{
		DisplayName: display,
		PublicKey:   keyLine,
		Color:       attribution.NextColor(memberColorsOf(members)),
		Role:        domain.RoleCollaborator,
	}
	if err := s.cfg.Store.CreateMember(ctx, m); err != nil {
		_ = restoreInvite(s.cfg.InvitesDir, code, rec)
		return nil, fmt.Errorf("sshd: invite join: %w", err)
	}
	slog.Info("sshd: invite join: collaborator registered",
		"member", m.ID, "key", fingerprintOf(keyLine), "display", display)
	return m, nil
}

// inviteIssuerAdmin reports whether issuer, or the member a merge made
// it, is an admin. A removed issuer is not.
func (s *Server) inviteIssuerAdmin(ctx context.Context, issuer domain.MemberID) (bool, error) {
	if ms, ok := s.cfg.Store.(store.MergeStore); ok {
		into, err := ms.MergedInto(ctx, issuer)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if into != "" {
			issuer = into
		}
	}
	m, err := s.cfg.Store.GetMember(ctx, issuer)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return m.Role == domain.RoleAdmin, nil
}

func restoreInvite(dir, code string, rec inviteRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(invitePath(dir, code), append(body, '\n'), 0o600)
}

// revokeInvitesBy deletes the open invite codes any of issuers minted.
func revokeInvitesBy(dir string, issuers ...domain.MemberID) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sshd: list invites: %w", err)
	}
	for _, e := range entries {
		rec, lerr := loadInvite(dir, e.Name())
		if lerr != nil || !slices.Contains(issuers, rec.IssuedBy) {
			continue
		}
		if berr := burnInvite(dir, e.Name()); berr != nil && !errors.Is(berr, os.ErrNotExist) {
			return fmt.Errorf("sshd: revoke invite: %w", berr)
		}
	}
	return nil
}
