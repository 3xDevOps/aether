package edgeproto

import (
	"strings"
	"testing"
	"time"
)

func TestAccountValidate(t *testing.T) {
	tests := []struct {
		name string
		a    Account
		ok   bool
	}{
		{"github", Account{Provider: ProviderGitHub, Subject: "1001", Login: "octo-fake", Email: "octo@example.com", Name: "Octo Fake"}, true},
		{"github without email", Account{Provider: ProviderGitHub, Subject: "1001", Login: "octo-fake"}, true},
		{"unknown provider", Account{Provider: "gitlab", Subject: "1"}, false},
		{"google, which v0.5.2-alpha.3 stored", Account{Provider: "google", Subject: "fake-sub"}, false},
		{"no provider", Account{Subject: "1"}, false},
		{"no subject", Account{Provider: ProviderGitHub}, false},
		{"email without at", Account{Provider: ProviderGitHub, Subject: "1", Email: "nobody"}, false},
		{"escape in name", Account{Provider: ProviderGitHub, Subject: "1", Name: "a\x1b[2Jb"}, false},
		{"newline in login", Account{Provider: ProviderGitHub, Subject: "1", Login: "a\nb"}, false},
		{"invalid utf-8", Account{Provider: ProviderGitHub, Subject: "1", Name: "\xff"}, false},
		{"long subject", Account{Provider: ProviderGitHub, Subject: strings.Repeat("1", maxShortText+1)}, false},
	}
	for _, tt := range tests {
		if err := tt.a.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok %v", tt.name, err, tt.ok)
		}
	}
}

func TestDeviceKeys(t *testing.T) {
	key := seedSigner(t, 1).PublicKey()
	line := DeviceKeyLine(key)
	if !strings.HasPrefix(line, "ssh-ed25519 ") || strings.Count(line, " ") != 1 || strings.Contains(line, "\n") {
		t.Fatalf("DeviceKeyLine = %q", line)
	}
	parsed, err := ParseDeviceKey(line + " laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !DeviceKeyMatches(line, parsed) || DeviceKeyMatches(line, seedSigner(t, 2).PublicKey()) {
		t.Fatal("DeviceKeyMatches disagrees with the key")
	}
	if DeviceKeyMatches("", key) {
		t.Fatal("empty grant key matched")
	}

	rsaLine := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC0" // truncated on purpose
	for _, bad := range []string{
		"",
		rsaLine,
		`command="true" ` + line,
		line + "\n" + DeviceKeyLine(seedSigner(t, 2).PublicKey()),
		strings.Repeat("x", maxShortText+1),
	} {
		if _, err := ParseDeviceKey(bad); err == nil {
			t.Errorf("ParseDeviceKey(%q) accepted it", bad)
		}
	}
}

func TestDirectoryEntryValidate(t *testing.T) {
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		e    DirectoryEntry
		ok   bool
	}{
		{"member", DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1", Role: "admin"}, true},
		{"login invitation", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "octo", Role: "viewer", ExpiresAt: exp}, true},
		{"email invitation any provider", DirectoryEntry{Kind: EntryInvitation, Email: "a@example.com", Role: "viewer", ExpiresAt: exp}, true},
		{"email invitation github", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Email: "a@example.com", Role: "viewer", ExpiresAt: exp}, true},
		{"email invitation google", DirectoryEntry{Kind: EntryInvitation, Provider: "google", Email: "a@example.com", Role: "viewer", ExpiresAt: exp}, false},
		{"member google", DirectoryEntry{Kind: EntryMember, Provider: "google", Subject: "1", Role: "admin"}, false},
		{"member without subject", DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Role: "admin"}, false},
		{"member with email", DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1", Email: "a@example.com", Role: "admin"}, false},
		{"member without role", DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1"}, false},
		{"invitation without expiry", DirectoryEntry{Kind: EntryInvitation, Email: "a@example.com", Role: "viewer"}, false},
		{"invitation with both", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "o", Email: "a@example.com", Role: "viewer", ExpiresAt: exp}, false},
		{"invitation with neither", DirectoryEntry{Kind: EntryInvitation, Role: "viewer", ExpiresAt: exp}, false},
		{"google login invitation", DirectoryEntry{Kind: EntryInvitation, Provider: "google", Login: "o", Role: "viewer", ExpiresAt: exp}, false},
		{"invitation with subject", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Subject: "1", Login: "o", Role: "viewer", ExpiresAt: exp}, false},
		{"unknown kind", DirectoryEntry{Kind: "owner", Provider: ProviderGitHub, Subject: "1", Role: "admin"}, false},
	}
	for _, tt := range tests {
		if err := tt.e.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok %v", tt.name, err, tt.ok)
		}
	}
}

func TestDirectoryEntryMatches(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	live, expired := now.Add(time.Hour), now

	confirmed := now.Add(-IdentityMaxAge)
	gh := Account{Provider: ProviderGitHub, Subject: "1001", Login: "Octo-Fake", Email: "Octo@Example.com", IdentityAt: confirmed}
	// google is an account of the provider builds from the v0.5.2-alpha.3
	// tag also offered: no current edge signs it in, and nothing matches it.
	google := Account{Provider: "google", Subject: "1001", Email: "octo@example.com", IdentityAt: confirmed}
	sameEmail := Account{Provider: ProviderGitHub, Subject: "4004", Login: "someone", Email: "octo@example.com", IdentityAt: confirmed}
	noEmail := Account{Provider: ProviderGitHub, Subject: "2002", Login: "other", IdentityAt: confirmed}
	stale := func(a Account) Account {
		a.IdentityAt = now.Add(-IdentityMaxAge - time.Second)
		return a
	}

	member := DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1001", Role: "admin"}
	loginInv := DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "octo-fake", Role: "viewer", ExpiresAt: live}
	emailInv := DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Email: "OCTO@example.COM", Role: "viewer", ExpiresAt: live}
	anyEmailInv := DirectoryEntry{Kind: EntryInvitation, Email: "octo@example.com", Role: "viewer", ExpiresAt: live}
	googleEmailInv := DirectoryEntry{Kind: EntryInvitation, Provider: "google", Email: "octo@example.com", Role: "viewer", ExpiresAt: live}
	kelvinInv := DirectoryEntry{Kind: EntryInvitation, Email: "Keith@example.com", Role: "viewer", ExpiresAt: live}
	kelvinLogin := DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "Keith", Role: "viewer", ExpiresAt: live}
	emptyEmailInv := DirectoryEntry{Kind: EntryInvitation, Email: "", Login: "", Role: "viewer", ExpiresAt: live}

	tests := []struct {
		name string
		e    DirectoryEntry
		a    Account
		want bool
	}{
		{"member by subject", member, gh, true},
		{"member, same subject other provider", member, google, false},
		{"member, other subject same email", member, Account{Provider: ProviderGitHub, Subject: "3003", Email: gh.Email, Login: gh.Login, IdentityAt: confirmed}, false},
		{"login invitation, case-insensitive", loginInv, gh, true},
		{"login invitation, other login", loginInv, noEmail, false},
		{"login invitation expired", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "octo-fake", Role: "viewer", ExpiresAt: expired}, gh, false},
		{"email invitation, github, case-insensitive", emailInv, gh, true},
		{"email invitation, another github account with that email", emailInv, sameEmail, true},
		{"email invitation without provider, github", anyEmailInv, gh, true},
		{"email invitation, google", emailInv, google, false},
		{"email invitation, account without verified email", emailInv, noEmail, false},
		{"google email invitation, github account", googleEmailInv, gh, false},
		{"kelvin sign does not fold to k", kelvinInv, Account{Provider: ProviderGitHub, Subject: "5", Email: "keith@example.com", IdentityAt: confirmed}, false},
		{"kelvin sign login does not fold to k", kelvinLogin, Account{Provider: ProviderGitHub, Subject: "5", Login: "keith", IdentityAt: confirmed}, false},
		{"invitation with nothing to match", emptyEmailInv, noEmail, false},
		{"email invitation expired", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Email: "octo@example.com", Role: "viewer", ExpiresAt: expired}, gh, false},
		{"unknown kind", DirectoryEntry{Kind: "owner", Provider: ProviderGitHub, Subject: "1001"}, gh, false},
		// A login or email the provider has not confirmed for a day may
		// belong to someone else by now; a member matches by subject.
		{"login invitation, stale identity", loginInv, stale(gh), false},
		{"email invitation, stale identity", emailInv, stale(gh), false},
		{"email invitation, never confirmed", emailInv, Account{Provider: ProviderGitHub, Subject: "1001", Email: "octo@example.com"}, false},
		{"member, stale identity", member, stale(gh), true},
	}
	for _, tt := range tests {
		if got := tt.e.Matches(tt.a, now); got != tt.want {
			t.Errorf("%s: Matches = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAccountIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		id := NewAccountID()
		if !ValidAccountID(id) || !strings.HasPrefix(id, "acct_") || len(id) != len("acct_")+26 {
			t.Fatalf("NewAccountID = %q", id)
		}
		if seen[id] {
			t.Fatalf("account id %q repeated", id)
		}
		seen[id] = true
	}
	id := NewAccountID()
	serverID := ServerID(seedSigner(t, 1).PublicKey())
	for _, bad := range []string{"", serverID, "acct_" + serverID[:25], "acct_" + serverID + "a", "ACCT_" + serverID, "acct_" + strings.ToUpper(serverID), id[:len(id)-1] + "1", "acct-" + serverID} {
		if ValidAccountID(bad) {
			t.Errorf("ValidAccountID(%q) = true", bad)
		}
	}
	if err := (AccountInfo{ID: "1", Account: Account{Provider: ProviderGitHub, Subject: "1"}}).Validate(); err == nil {
		t.Fatal("AccountInfo.Validate accepted a malformed id")
	}
}

func TestPrincipalValidate(t *testing.T) {
	owner := Account{Provider: ProviderGitHub, Subject: "1001", Login: "owner-fake", Email: "owner@example.com"}
	if p := AccountPrincipal(owner); p != (Principal{Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "1001"}) || p.Validate() != nil {
		t.Fatalf("AccountPrincipal = %+v", p)
	}
	for name, p := range map[string]Principal{
		"no type":          {Provider: ProviderGitHub, Subject: "1"},
		"unknown type":     {Type: "org", Provider: ProviderGitHub, Subject: "1"},
		"team, reserved":   {Type: PrincipalTeam, Provider: ProviderGitHub, Subject: "1"},
		"account, no sub":  {Type: PrincipalAccount, Provider: ProviderGitHub},
		"account, no prov": {Type: PrincipalAccount, Subject: "1"},
		"account, google":  {Type: PrincipalAccount, Provider: "google", Subject: "1"},
		"escape in sub":    {Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "1\x1b[2J"},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}

func TestParseDeviceStatus(t *testing.T) {
	for _, s := range []DeviceStatus{DeviceStatusRegistered, DeviceStatusApproved, DeviceStatusPending, DeviceStatusRevoked} {
		if got, err := ParseDeviceStatus(string(s)); err != nil || got != s {
			t.Errorf("ParseDeviceStatus(%q) = %q, %v", s, got, err)
		}
	}
	for _, in := range []string{"", "Approved", "authorization_pending", "active"} {
		if got, err := ParseDeviceStatus(in); err == nil {
			t.Errorf("ParseDeviceStatus(%q) = %q, want an error", in, got)
		}
	}
}
