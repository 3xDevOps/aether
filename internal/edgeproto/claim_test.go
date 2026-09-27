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
		prefix, secret, ok := strings.Cut(code, "-")
		if !ok || prefix != id[:ClaimPrefixLength] || len(secret) != 16 || !isLowerBase32(secret) {
			t.Fatalf("NewClaimCode = %q", code)
		}
		if seen[secret] {
			t.Fatalf("secret %q repeated", secret)
		}
		seen[secret] = true
		normalized, gotPrefix, err := ParseClaimCode(code)
		if err != nil || normalized != code || gotPrefix != prefix {
			t.Fatalf("ParseClaimCode(%q) = %q, %q, %v", code, normalized, gotPrefix, err)
		}
	}
	if _, err := NewClaimCode("not-an-id"); err == nil {
		t.Fatal("NewClaimCode accepted a bad server id")
	}
}

func TestParseClaimCode(t *testing.T) {
	const code = "wqc4lsjv-abcdefghijklmnop"
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{code, code, true},
		{"  WQC4LSJV-ABCDEFGHIJKLMNOP\n", code, true},
		{"wqc4lsjv-abcdefghijklmno", "", false},
		{"wqc4lsjv-abcdefghijklmnopq", "", false},
		{"wqc4lsj-abcdefghijklmnopq", "", false},
		{"wqc4lsjvabcdefghijklmnop", "", false},
		{"wqc4lsjv-abcdefghijklmn0p", "", false},
		{"wqc4lsjv-abcdefgh-jklmnop", "", false},
		{"wqc4lsjv--bcdefghijklmnop", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, prefix, err := ParseClaimCode(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseClaimCode(%q) = %q, %v; want %q, ok %v", tt.in, got, err, tt.want, tt.ok)
		}
		if tt.ok && prefix != "wqc4lsjv" {
			t.Errorf("ParseClaimCode(%q) prefix = %q", tt.in, prefix)
		}
	}
}

func TestClaimCodeEqual(t *testing.T) {
	const issued = "wqc4lsjv-abcdefghijklmnop"
	tests := []struct {
		presented string
		want      bool
	}{
		{issued, true},
		{" WQC4LSJV-abcdefghijklmnop ", true},
		{"wqc4lsjv-abcdefghijklmnoq", false},
		{"aqc4lsjv-abcdefghijklmnop", false},
		{"wqc4lsjv-abcdefghijklmno", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ClaimCodeEqual(issued, tt.presented); got != tt.want {
			t.Errorf("ClaimCodeEqual(%q) = %v, want %v", tt.presented, got, tt.want)
		}
	}
}
