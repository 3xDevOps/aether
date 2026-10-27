package edgeproto

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

// TestReviewerAssertion signs and verifies a ConnectorAssertion for the
// reviewer account, and refuses one whose reviewer account has an email,
// another login or a login that is no reviewer login.
func TestReviewerAssertion(t *testing.T) {
	key := edgeKey(1)
	now := time.Now()
	server := ServerID(seedSigner(t, 1).PublicKey())
	assertion := func(a Account) ConnectorAssertion {
		return ConnectorAssertion{Issuer: testOrigin, ServerID: server, Nonce: strings.Repeat("ab", 16), Account: a,
			ClientID: "https://chatgpt.com/client.json", Challenge: "challenge", RedirectURI: "https://chatgpt.com/cb",
			IssuedAt: now, ExpiresAt: now.Add(ConnectorAssertionTTL)}
	}
	reviewer := Account{Provider: ProviderReviewer, Subject: "dir-review", Login: "dir-review"}
	signed, err := SignConnectorAssertion(key, assertion(reviewer))
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyConnectorAssertion(key.Public().(ed25519.PublicKey), signed, testOrigin, server,
		"https://chatgpt.com/client.json", "challenge", "https://chatgpt.com/cb", now)
	if err != nil || got.Account != reviewer {
		t.Fatalf("VerifyConnectorAssertion = %+v, %v", got.Account, err)
	}
	for _, a := range []Account{
		{Provider: ProviderReviewer, Subject: "dir-review", Login: "dir-review", Email: "r@example.com"},
		{Provider: ProviderReviewer, Subject: "dir-review", Login: "octo"},
		{Provider: ProviderReviewer, Subject: "Dir_Review", Login: "Dir_Review"},
	} {
		if _, err := SignConnectorAssertion(key, assertion(a)); err == nil {
			t.Errorf("SignConnectorAssertion(%+v) succeeded", a)
		}
	}
}
