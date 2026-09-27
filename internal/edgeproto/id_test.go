package edgeproto

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func seedSigner(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(s))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestServerIDVector(t *testing.T) {
	// Pins the derivation: a change here re-keys every linked server.
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ServerID(signer.PublicKey()), "wqc4lsjvzdzrwq3k5dabdtajwj"; got != want {
		t.Fatalf("ServerID = %q, want %q", got, want)
	}
}

func TestServerIDShape(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecSigner, err := ssh.NewSignerFromKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	a, b := seedSigner(t, 1).PublicKey(), seedSigner(t, 2).PublicKey()
	for _, key := range []ssh.PublicKey{a, b, ecSigner.PublicKey()} {
		id := ServerID(key)
		if !ValidServerID(id) {
			t.Fatalf("ServerID %q is not valid", id)
		}
		if !HostKeyMatches(key, id) {
			t.Fatalf("HostKeyMatches(own id) = false")
		}
	}
	if ServerID(a) == ServerID(b) {
		t.Fatal("two keys derived one id")
	}
	if HostKeyMatches(a, ServerID(b)) {
		t.Fatal("key matched another key's id")
	}
	if HostKeyMatches(a, strings.ToUpper(ServerID(a))) {
		t.Fatal("uppercase id matched")
	}
}

func TestValidServerID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"wqc4lsjvzdzrwq3k5dabdtajwj", true},
		{"abcdefghijklmnopqrstuvwxyz", true},
		{"22222222222222222222222222", true},
		{"wqc4lsjvzdzrwq3k5dabdtajw", false},
		{"wqc4lsjvzdzrwq3k5dabdtajwjj", false},
		{"WQC4LSJVZDZRWQ3K5DABDTAJWJ", false},
		{"wqc4lsjvzdzrwq3k5dabdtajw1", false},
		{"wqc4lsjvzdzrwq3k5dabdtajw8", false},
		{"wqc4lsjvzdzrwq3k5dabdtaj/.", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidServerID(tt.id); got != tt.want {
			t.Errorf("ValidServerID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestValidServerDomain(t *testing.T) {
	longest := strings.Repeat(strings.Repeat("a", 62)+".", 3) + strings.Repeat("a", 253-ServerIDLength-1-3*63)
	for _, d := range []string{"servers.example.com", "a-1.b2.test", longest} {
		if !ValidServerDomain(d) {
			t.Errorf("ValidServerDomain(%q) = false", d)
		}
	}
	for _, d := range []string{"", "localhost", "Servers.example.com", "servers.example.com.", "-a.example.com",
		"a_b.example.com", "https://servers.example.com", "servers.example.com:443", longest + "a"} {
		if ValidServerDomain(d) {
			t.Errorf("ValidServerDomain(%q) = true", d)
		}
	}
}

func TestServerIDFromHostname(t *testing.T) {
	const id = "wqc4lsjvzdzrwq3k5dabdtajwj"
	if got := ServerHostname(id, "Servers.Example"); got != id+".servers.example" {
		t.Fatalf("ServerHostname = %q", got)
	}
	tests := []struct {
		host string
		want string
		ok   bool
	}{
		{id + ".servers.example", id, true},
		{strings.ToUpper(id) + ".SERVERS.example", id, true},
		{"servers.example", "", false},
		{"x." + id + ".servers.example", "", false},
		{id + ".servers.example.evil", "", false},
		{id + "servers.example", "", false},
		{id + ".evilservers.example", "", false},
		{"short.servers.example", "", false},
		{id + ".servers.example.", "", false},
	}
	for _, tt := range tests {
		got, ok := ServerIDFromHostname(tt.host, "servers.example")
		if got != tt.want || ok != tt.ok {
			t.Errorf("ServerIDFromHostname(%q) = %q, %v; want %q, %v", tt.host, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTokens(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok := NewToken()
		if !ValidToken(tok) || seen[tok] {
			t.Fatalf("NewToken returned %q (valid %v, repeated %v)", tok, ValidToken(tok), seen[tok])
		}
		seen[tok] = true
		if h := HashToken(tok); len(h) != 64 || h == HashToken(NewToken()) {
			t.Fatalf("HashToken = %q", h)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("+", 43), strings.Repeat("a", 42) + "=", strings.Repeat("a", 42) + "B"} {
		if ValidToken(bad) {
			t.Errorf("ValidToken(%q) = true", bad)
		}
	}
	// sha256("fake-token"), so a stored hash stays valid across releases.
	if got, want := HashToken("fake-token"), "e1466187c844c921b622aff2197444cfdc2c87489f7a6e71cef47b31a1602ced"; got != want {
		t.Fatalf("HashToken = %q, want %q", got, want)
	}
}

func TestConnIDs(t *testing.T) {
	a, b := NewConnID(), NewConnID()
	if !ValidConnID(a) || !ValidConnID(b) || a == b {
		t.Fatalf("NewConnID = %q, %q", a, b)
	}
	for _, bad := range []string{"", strings.Repeat("0", 31), strings.Repeat("0", 33), strings.Repeat("A", 32), strings.Repeat("0", 31) + "/", "../../v1/connect/" + strings.Repeat("0", 15)} {
		if ValidConnID(bad) {
			t.Errorf("ValidConnID(%q) = true", bad)
		}
	}
}
