package edgeproto

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// Claim code shape: "<server id>-<secret>", the secret being 16 base32
// characters (80 bits) from crypto/rand. The whole id is in the code so
// that the claimant can check the server the edge answers with is exactly
// the one the code names; a prefix could be matched by a ground host key.
const (
	claimSecretLength = 16
	claimCodeLength   = ServerIDLength + 1 + claimSecretLength
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
	return serverID + "-" + strings.ToLower(base32NoPad.EncodeToString(secret[:])), nil
}

// ParseClaimCode normalizes a claim code as a person typed it (surrounding
// space, any case) and returns it with the server id it names.
func ParseClaimCode(code string) (normalized, serverID string, err error) {
	normalized = strings.ToLower(strings.TrimSpace(code))
	serverID, secret, ok := strings.Cut(normalized, "-")
	if !ok || len(normalized) != claimCodeLength || !ValidServerID(serverID) || !isLowerBase32(secret) {
		return "", "", fmt.Errorf("edgeproto: claim code must look like <%d-character server id>-%s",
			ServerIDLength, strings.Repeat("x", claimSecretLength))
	}
	return normalized, serverID, nil
}
