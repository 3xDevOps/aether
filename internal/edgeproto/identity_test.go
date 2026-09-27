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
		{"google without email", Account{Provider: ProviderGoogle, Subject: "fake-sub"}, true},
		{"unknown provider", Account{Provider: "gitlab", Subject: "1"}, false},
		{"no subject", Account{Provider: ProviderGitHub}, false},
		{"google login", Account{Provider: ProviderGoogle, Subject: "1", Login: "x"}, false},
		{"email without at", Account{Provider: ProviderGoogle, Subject: "1", Email: "nobody"}, false},
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
		{"email invitation google", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGoogle, Email: "a@example.com", Role: "viewer", ExpiresAt: exp}, true},
		{"member without subject", DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Role: "admin"}, false},
		{"member with email", DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1", Email: "a@example.com", Role: "admin"}, false},
		{"member without role", DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1"}, false},
		{"invitation without expiry", DirectoryEntry{Kind: EntryInvitation, Email: "a@example.com", Role: "viewer"}, false},
		{"invitation with both", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "o", Email: "a@example.com", Role: "viewer", ExpiresAt: exp}, false},
		{"invitation with neither", DirectoryEntry{Kind: EntryInvitation, Role: "viewer", ExpiresAt: exp}, false},
		{"google login invitation", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGoogle, Login: "o", Role: "viewer", ExpiresAt: exp}, false},
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
	google := Account{Provider: ProviderGoogle, Subject: "1001", Email: "octo@example.com", IdentityAt: confirmed}
	noEmail := Account{Provider: ProviderGitHub, Subject: "2002", Login: "other", IdentityAt: confirmed}
	stale := func(a Account) Account {
		a.IdentityAt = now.Add(-IdentityMaxAge - time.Second)
		return a
	}

	member := DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1001", Role: "admin"}
	loginInv := DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "octo-fake", Role: "viewer", ExpiresAt: live}
	emailInv := DirectoryEntry{Kind: EntryInvitation, Email: "OCTO@example.COM", Role: "viewer", ExpiresAt: live}
	googleEmailInv := DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGoogle, Email: "octo@example.com", Role: "viewer", ExpiresAt: live}
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
		{"login invitation, google account", loginInv, Account{Provider: ProviderGoogle, Subject: "9", Email: "x@example.com", IdentityAt: confirmed}, false},
		{"login invitation, other login", loginInv, noEmail, false},
		{"login invitation expired", DirectoryEntry{Kind: EntryInvitation, Provider: ProviderGitHub, Login: "octo-fake", Role: "viewer", ExpiresAt: expired}, gh, false},
		{"email invitation, github, case-insensitive", emailInv, gh, true},
		{"email invitation, google", emailInv, google, true},
		{"email invitation, account without verified email", emailInv, noEmail, false},
		{"google email invitation, github account", googleEmailInv, gh, false},
		{"google email invitation, google account", googleEmailInv, google, true},
		{"kelvin sign does not fold to k", kelvinInv, Account{Provider: ProviderGoogle, Subject: "5", Email: "keith@example.com", IdentityAt: confirmed}, false},
		{"kelvin sign login does not fold to k", kelvinLogin, Account{Provider: ProviderGitHub, Subject: "5", Login: "keith", IdentityAt: confirmed}, false},
		{"invitation with nothing to match", emptyEmailInv, noEmail, false},
		{"email invitation expired", DirectoryEntry{Kind: EntryInvitation, Email: "octo@example.com", Role: "viewer", ExpiresAt: expired}, google, false},
		{"unknown kind", DirectoryEntry{Kind: "owner", Provider: ProviderGitHub, Subject: "1001"}, gh, false},
		// A login or email the provider has not confirmed for a day may
		// belong to someone else by now; a member matches by subject.
		{"login invitation, stale identity", loginInv, stale(gh), false},
		{"email invitation, stale identity", emailInv, stale(google), false},
		{"email invitation, never confirmed", emailInv, Account{Provider: ProviderGoogle, Subject: "1001", Email: "octo@example.com"}, false},
		{"member, stale identity", member, stale(gh), true},
	}
	for _, tt := range tests {
		if got := tt.e.Matches(tt.a, now); got != tt.want {
			t.Errorf("%s: Matches = %v, want %v", tt.name, got, tt.want)
		}
	}
}
