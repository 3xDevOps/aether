package edgeproto

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"
)

// Claim code shape: "<first 8 characters of the server id>-<secret>", the
// secret being 16 base32 characters (80 bits) from crypto/rand.
const (
	ClaimPrefixLength = 8
	claimSecretLength = 16
	claimCodeLength   = ClaimPrefixLength + 1 + claimSecretLength
)

// A claim code is valid for ClaimCodeTTL and ClaimCodeAttempts attempts.
const (
	ClaimCodeTTL      = 30 * time.Minute
	ClaimCodeAttempts = 5
)

// NewClaimCode returns a fresh claim code for serverID.
func NewClaimCode(serverID string) (string, error) {
	if !ValidServerID(serverID) {
		return "", fmt.Errorf("edgeproto: invalid server id %q", serverID)
	}
	var secret [claimSecretLength * 5 / 8]byte
	_, _ = rand.Read(secret[:]) // crypto/rand.Read never returns an error.
	return serverID[:ClaimPrefixLength] + "-" + strings.ToLower(base32NoPad.EncodeToString(secret[:])), nil
}

// ParseClaimCode normalizes a claim code as a person typed it (surrounding
// space, any case) and returns it with its server id prefix.
func ParseClaimCode(code string) (normalized, prefix string, err error) {
	normalized = strings.ToLower(strings.TrimSpace(code))
	prefix, secret, ok := strings.Cut(normalized, "-")
	if !ok || len(normalized) != claimCodeLength || len(prefix) != ClaimPrefixLength ||
		!isLowerBase32(prefix) || !isLowerBase32(secret) {
		return "", "", fmt.Errorf("edgeproto: claim code must look like %s-%s",
			strings.Repeat("x", ClaimPrefixLength), strings.Repeat("x", claimSecretLength))
	}
	return normalized, prefix, nil
}

// ClaimCodeEqual compares the code a server issued with one presented to
// it, in constant time.
func ClaimCodeEqual(issued, presented string) bool {
	normalized, _, err := ParseClaimCode(presented)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(issued), []byte(normalized)) == 1
}
