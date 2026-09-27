package edgeproto

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// NewVerifier returns a PKCE code verifier for one web sign-in. It is a
// token, 43 characters, the minimum length RFC 7636 allows.
func NewVerifier() string { return NewToken() }

// PKCEChallenge is the S256 challenge of verifier:
// base64url-nopad(sha256(verifier)).
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// VerifyPKCE reports, in constant time, whether verifier answers challenge.
func VerifyPKCE(challenge, verifier string) bool {
	return subtle.ConstantTimeCompare([]byte(PKCEChallenge(verifier)), []byte(challenge)) == 1
}
