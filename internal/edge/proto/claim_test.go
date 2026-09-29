package edgeproto

import (
	"strings"
	"testing"
)

func TestNewClaimCode(t *testing.T) {
	id := ServerID(seedSigner(t, 1).PublicKey())
	seen := map[string]bool{}
	for range 50 {
		code, err := NewClaimCode(id)
		if err != nil {
			t.Fatal(err)
		}
		gotID, secret, ok := strings.Cut(code, "-")
		if !ok || gotID != id || len(secret) != 16 || !isLowerBase32(secret) {
			t.Fatalf("NewClaimCode = %q", code)
		}
		if seen[secret] {
			t.Fatalf("secret %q repeated", secret)
		}
		seen[secret] = true
		normalized, parsedID, err := ParseClaimCode(code)
		if err != nil || normalized != code || parsedID != id {
			t.Fatalf("ParseClaimCode(%q) = %q, %q, %v", code, normalized, parsedID, err)
		}
	}
	if _, err := NewClaimCode("not-an-id"); err == nil {
		t.Fatal("NewClaimCode accepted a bad server id")
	}
}

func TestParseClaimCode(t *testing.T) {
	const id = "wqc4lsjvabcdefghijklmnopqr"
	const code = id + "-abcdefghijklmnop"
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{code, code, true},
		{"  " + strings.ToUpper(code) + "\n", code, true},
		{id + "-abcdefghijklmno", "", false},
		{id + "-abcdefghijklmnopq", "", false},
		{id[:25] + "-abcdefghijklmnopq", "", false},
		{"wqc4lsjv-abcdefghijklmnop", "", false},
		{id + "abcdefghijklmnop", "", false},
		{id + "-abcdefghijklmn0p", "", false},
		{id + "-abcdefgh-jklmnop", "", false},
		{id + "--bcdefghijklmnop", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, gotID, err := ParseClaimCode(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseClaimCode(%q) = %q, %v; want %q, ok %v", tt.in, got, err, tt.want, tt.ok)
		}
		if tt.ok && gotID != id {
			t.Errorf("ParseClaimCode(%q) server id = %q", tt.in, gotID)
		}
	}
}

func TestClaimUser(t *testing.T) {
	id := ServerID(seedSigner(t, 1).PublicKey())
	code, err := NewClaimCode(id)
	if err != nil {
		t.Fatal(err)
	}
	owner := Principal{Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "1001"}
	user, gotID, err := ClaimUser("  "+strings.ToUpper(code)+"\n", owner)
	if err != nil || user != "claim:"+code+":github:1001" || gotID != id {
		t.Fatalf("ClaimUser = %q, %q, %v", user, gotID, err)
	}
	if got, gotOwner, ok := ParseClaimUser(user); !ok || got != code || gotOwner != owner {
		t.Fatalf("ParseClaimUser(%q) = %q, %+v, %v", user, got, gotOwner, ok)
	}
	// A subject may hold a colon; the provider may not.
	colon := Principal{Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "a:b"}
	if user, _, err := ClaimUser(code, colon); err != nil {
		t.Fatal(err)
	} else if _, got, ok := ParseClaimUser(user); !ok || got != colon {
		t.Fatalf("ParseClaimUser(%q) = %+v, %v", user, got, ok)
	}
	if _, _, err := ClaimUser("not-a-code", owner); err == nil {
		t.Fatal("ClaimUser accepted a malformed code")
	}
	for _, bad := range []Principal{
		{Type: PrincipalTeam, Provider: ProviderGitHub, Subject: "1001"},
		{Type: PrincipalAccount, Provider: "gitlab", Subject: "1001"},
		{Type: PrincipalAccount, Provider: ProviderGitHub},
	} {
		if _, _, err := ClaimUser(code, bad); err == nil {
			t.Errorf("ClaimUser accepted owner %+v", bad)
		}
	}

	for _, bad := range []string{
		"",
		"claim:",
		code,
		"claim:" + code,
		"claim:" + code + ":github",
		"claim:" + code + ":github:",
		"claim:" + code + ":gitlab:1001",
		"claim:" + code + ":github:10\x0001",
		"invite:" + code + ":github:1001",
		"claim:" + strings.ToUpper(code) + ":github:1001",
		"claim: " + code + ":github:1001",
		"claim:" + code[:len(code)-1] + ":github:1001",
		"claim:" + code + "a:github:1001",
		"claim:" + id + "-abcdefghijklmn0p:github:1001",
		"claim:" + code + ":github:" + strings.Repeat("1", 1<<16),
	} {
		if got, _, ok := ParseClaimUser(bad); ok {
			t.Errorf("ParseClaimUser(%q) = %q, want refused", bad, got)
		}
	}
}
