package edgeproto

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func edgeKey(seed byte) ed25519.PrivateKey {
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	return ed25519.NewKeyFromSeed(s)
}

func testGrant(t *testing.T, now time.Time) Grant {
	t.Helper()
	return Grant{
		Issuer:      testOrigin,
		ServerID:    ServerID(seedSigner(t, 1).PublicKey()),
		ConnID:      strings.Repeat("ab", 16),
		Kind:        KindSSH,
		Account:     Account{Provider: ProviderGitHub, Subject: "1001", Login: "octo-fake"},
		DeviceID:    "dev-fake-1",
		DeviceKey:   DeviceKeyLine(seedSigner(t, 9).PublicKey()),
		DeviceLabel: "laptop",
		IssuedAt:    now,
		ExpiresAt:   now.Add(GrantTTL),
	}
}

func scopeOf(g Grant) GrantScope {
	return GrantScope{Issuer: g.Issuer, ServerID: g.ServerID, ConnID: g.ConnID, Kind: g.Kind}
}

func TestGrantRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.FixedZone("x", 3600))
	g := testGrant(t, now)
	signed, err := SignGrant(edgeKey(1), g)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyGrant(edgeKey(1).Public().(ed25519.PublicKey), signed, scopeOf(g), now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IssuedAt.Equal(g.IssuedAt) || got.Issuer != g.Issuer || got.Account != g.Account || got.DeviceKey != g.DeviceKey || got.DeviceID != g.DeviceID {
		t.Fatalf("round trip = %+v, want %+v", got, g)
	}

	payload, _, _ := strings.Cut(signed, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"issued_at":"2026-09-27T11:00:00Z"`) {
		t.Fatalf("payload times are not UTC: %s", raw)
	}
}

func TestVerifyGrantRefusals(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	pub := edgeKey(1).Public().(ed25519.PublicKey)
	g := testGrant(t, now)
	signed, err := SignGrant(edgeKey(1), g)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := SignGrant(edgeKey(2), g)
	if err != nil {
		t.Fatal(err)
	}
	payload, sig, _ := strings.Cut(signed, ".")
	rawSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		t.Fatal(err)
	}
	shortSig := payload + "." + base64.RawURLEncoding.EncodeToString(rawSig[:ed25519.SignatureSize-1])

	tampered := g
	tampered.Account.Subject = "2002"
	tamperedJSON, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	swapped := base64.RawURLEncoding.EncodeToString(tamperedJSON) + "." + sig

	otherIssuer := scopeOf(g)
	otherIssuer.Issuer = "https://other-edge.example"
	otherServer := scopeOf(g)
	otherServer.ServerID = ServerID(seedSigner(t, 2).PublicKey())
	otherConn := scopeOf(g)
	otherConn.ConnID = strings.Repeat("cd", 16)
	otherKind := scopeOf(g)
	otherKind.Kind = KindClaim

	tests := []struct {
		name   string
		signed string
		scope  GrantScope
		now    time.Time
		want   error
	}{
		{"valid at issue", signed, scopeOf(g), now, nil},
		{"valid within skew after expiry", signed, scopeOf(g), now.Add(GrantTTL + ClockSkew), nil},
		{"valid within skew before issue", signed, scopeOf(g), now.Add(-ClockSkew), nil},
		{"expired", signed, scopeOf(g), now.Add(GrantTTL + ClockSkew + time.Second), ErrGrantExpired},
		{"not yet valid", signed, scopeOf(g), now.Add(-ClockSkew - time.Second), ErrGrantNotYetValid},
		{"forged by another key", forged, scopeOf(g), now, ErrGrantSignature},
		{"payload swapped under signature", swapped, scopeOf(g), now, ErrGrantSignature},
		{"wrong issuer", signed, otherIssuer, now, ErrGrantIssuer},
		{"wrong server", signed, otherServer, now, ErrGrantServer},
		{"wrong connection", signed, otherConn, now, ErrGrantConn},
		{"wrong kind", signed, otherKind, now, ErrGrantKind},
		{"no signature", payload, scopeOf(g), now, ErrGrantMalformed},
		{"bad base64", payload + ".!!!", scopeOf(g), now, ErrGrantMalformed},
		{"truncated signature", shortSig, scopeOf(g), now, ErrGrantSignature},
		{"extra segment", signed + ".x", scopeOf(g), now, ErrGrantMalformed},
		{"oversize", strings.Repeat("a", MaxGrantSize+1), scopeOf(g), now, ErrGrantMalformed},
		{"empty", "", scopeOf(g), now, ErrGrantMalformed},
	}
	for _, tt := range tests {
		_, err := VerifyGrant(pub, tt.signed, tt.scope, tt.now)
		if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
			t.Errorf("%s: VerifyGrant = %v, want %v", tt.name, err, tt.want)
		}
	}

	if _, err := VerifyGrant(pub[:16], signed, scopeOf(g), now); err == nil {
		t.Fatal("VerifyGrant accepted a short edge key")
	}
}

// A grant the edge signed but whose content is invalid is still refused,
// and SignGrant refuses to produce it in the first place.
func TestGrantContentRules(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	priv := edgeKey(1)
	pub := priv.Public().(ed25519.PublicKey)
	rsaKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC0"

	tests := []struct {
		name   string
		mutate func(*Grant)
		ok     bool
	}{
		{"ssh grant", func(*Grant) {}, true},
		{"claim grant", func(g *Grant) { g.Kind = KindClaim }, true},
		{"claim grant without key", func(g *Grant) { g.Kind, g.DeviceKey = KindClaim, "" }, false},
		{"ssh grant without key", func(g *Grant) { g.DeviceKey = "" }, false},
		{"ssh grant with unparseable key", func(g *Grant) { g.DeviceKey = "ssh-ed25519 !!!" }, false},
		{"ssh grant with rsa key", func(g *Grant) { g.DeviceKey = rsaKey }, false},
		{"web grant, a kind no longer signed", func(g *Grant) { g.Kind, g.DeviceKey = "web", "" }, false},
		{"unknown kind", func(g *Grant) { g.Kind = "admin" }, false},
		{"lifetime over TTL", func(g *Grant) { g.ExpiresAt = g.IssuedAt.Add(GrantTTL + time.Second) }, false},
		{"expires before issue", func(g *Grant) { g.ExpiresAt = g.IssuedAt.Add(-time.Second) }, false},
		{"zero issue time", func(g *Grant) { g.IssuedAt = time.Time{} }, false},
		{"no issuer", func(g *Grant) { g.Issuer = "" }, false},
		{"issuer with a path", func(g *Grant) { g.Issuer = testOrigin + "/v1" }, false},
		{"issuer not canonical", func(g *Grant) { g.Issuer = "https://EDGE.example" }, false},
		{"bad server id", func(g *Grant) { g.ServerID = "short" }, false},
		{"bad conn id", func(g *Grant) { g.ConnID = "../x" }, false},
		{"no device id", func(g *Grant) { g.DeviceID = "" }, false},
		{"escape in label", func(g *Grant) { g.DeviceLabel = "\x1b]0;x\x07" }, false},
		{"invalid account", func(g *Grant) { g.Account.Provider = "" }, false},
	}
	for _, tt := range tests {
		g := testGrant(t, now)
		tt.mutate(&g)
		signed, err := SignGrant(priv, g)
		if (err == nil) != tt.ok {
			t.Errorf("%s: SignGrant = %v, want ok %v", tt.name, err, tt.ok)
		}
		if tt.ok {
			if _, verr := VerifyGrant(pub, signed, scopeOf(g), now); verr != nil {
				t.Errorf("%s: VerifyGrant = %v", tt.name, verr)
			}
			continue
		}
		// Sign the invalid payload directly, as a buggy or hostile edge would.
		payload, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		raw := base64.RawURLEncoding.EncodeToString(payload) + "." +
			base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, grantMessage(payload)))
		if _, err := VerifyGrant(pub, raw, scopeOf(g), now); !errors.Is(err, ErrGrantMalformed) {
			t.Errorf("%s: VerifyGrant of edge-signed invalid grant = %v", tt.name, err)
		}
	}
}

// The grant signature is domain-separated from anything else an Ed25519
// key could sign: it is not valid over the bare payload.
func TestGrantSignatureIsDomainSeparated(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	signed, err := SignGrant(edgeKey(1), testGrant(t, now))
	if err != nil {
		t.Fatal(err)
	}
	encPayload, encSig, _ := strings.Cut(signed, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(encPayload)
	sig, _ := base64.RawURLEncoding.DecodeString(encSig)
	if ed25519.Verify(edgeKey(1).Public().(ed25519.PublicKey), payload, sig) {
		t.Fatal("grant signature verifies over the bare payload")
	}
}

func TestEdgeKeyFingerprint(t *testing.T) {
	pub := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	// Same value ssh-keygen -l prints for this key.
	if got, want := EdgeKeyFingerprint(pub), "SHA256:tAXFyTXI8xtDaujAEcwJslAYc9/6FKcUkd2Lw0xDhPo"; got != want {
		t.Fatalf("EdgeKeyFingerprint = %q, want %q", got, want)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if EdgeKeyFingerprint(pub) != ssh.FingerprintSHA256(sshPub) {
		t.Fatal("EdgeKeyFingerprint differs from ssh.FingerprintSHA256")
	}
	if EdgeKeyFingerprint(pub) == EdgeKeyFingerprint(edgeKey(1).Public().(ed25519.PublicKey)) {
		t.Fatal("two keys share a fingerprint")
	}
}
