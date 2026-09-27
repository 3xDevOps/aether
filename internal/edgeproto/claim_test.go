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
