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

// claimUserPrefix starts the SSH user name of a claim connection, the way
// "invite:" starts one that carries an invite code. The user name travels
// inside the encrypted SSH transport, after the client checked the host
// key against the server id in the code, so the edge relaying the
// connection never sees the code.
const claimUserPrefix = "claim:"

// ClaimUser returns the SSH user name that presents code, as a person
// typed it, on a claim connection by owner, the account the client is
// signed in as, and the server id the client pins the host key to. The
// client's SSH signature covers the user name, so the server learns from
// the client, not from the edge's grant, which account the claim is for,
// and refuses a grant that names another.
func ClaimUser(code string, owner Principal) (user, serverID string, err error) {
	normalized, serverID, err := ParseClaimCode(code)
	if err != nil {
		return "", "", err
	}
	if owner.Type != PrincipalAccount {
		return "", "", fmt.Errorf("edgeproto: a claim makes an account the owner, not a %q", owner.Type)
	}
	if err := owner.Validate(); err != nil {
		return "", "", err
	}
	if strings.Contains(owner.Provider, ":") {
		return "", "", fmt.Errorf("edgeproto: provider %q has a colon", owner.Provider)
	}
	return claimUserPrefix + normalized + ":" + owner.Provider + ":" + owner.Subject, serverID, nil
}

// ParseClaimUser returns the claim code and the claiming account in an SSH
// user name, accepting only the exact form ClaimUser produces. The code is
// a secret: never log the user name of a claim connection.
func ParseClaimUser(user string) (code string, owner Principal, ok bool) {
	rest, found := strings.CutPrefix(user, claimUserPrefix)
	if !found {
		return "", Principal{}, false
	}
	code, rest, found = strings.Cut(rest, ":")
	if !found || len(code) != claimCodeLength {
		return "", Principal{}, false
	}
	if normalized, _, err := ParseClaimCode(code); err != nil || normalized != code {
		return "", Principal{}, false
	}
	provider, subject, found := strings.Cut(rest, ":")
	owner = Principal{Type: PrincipalAccount, Provider: provider, Subject: subject}
	if !found || owner.Validate() != nil {
		return "", Principal{}, false
	}
	return code, owner, true
}
