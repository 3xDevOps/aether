package edgeproto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ServerIDLength is the length of a server id: 26 base32 characters, 130
// bits of the host key hash.
const ServerIDLength = 26

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// ServerID derives a server's id from its SSH host public key:
// lower(base32-nopad(sha256(wire bytes)))[:26].
func ServerID(hostKey ssh.PublicKey) string {
	sum := sha256.Sum256(hostKey.Marshal())
	return strings.ToLower(base32NoPad.EncodeToString(sum[:]))[:ServerIDLength]
}

// ValidServerID reports whether id has the shape of a server id.
func ValidServerID(id string) bool {
	return len(id) == ServerIDLength && isLowerBase32(id)
}

func isLowerBase32(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// HostKeyMatches reports whether hostKey derives serverID. Clients call it
// from their SSH HostKeyCallback on every path, direct or relayed.
func HostKeyMatches(hostKey ssh.PublicKey, serverID string) bool {
	return subtle.ConstantTimeCompare([]byte(ServerID(hostKey)), []byte(serverID)) == 1
}

// ServerHostname is the dashboard hostname of a server under the edge's
// server domain.
func ServerHostname(serverID, domain string) string {
	return serverID + "." + strings.ToLower(domain)
}

// ServerIDFromHostname returns the server id of a hostname that is exactly
// "<server id>.<domain>", compared case-insensitively.
func ServerIDFromHostname(host, domain string) (string, bool) {
	id, ok := strings.CutSuffix(strings.ToLower(host), "."+strings.ToLower(domain))
	if !ok || !ValidServerID(id) {
		return "", false
	}
	return id, true
}

// TokenLength is the length of a token from NewToken.
const TokenLength = 43

// NewToken returns 32 bytes from crypto/rand, base64url without padding.
// Device tokens, data-socket tickets, web sign-in codes, PKCE verifiers
// and session cookies are tokens. Store only HashToken of a bearer token.
func NewToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error.
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// ValidToken reports whether s has the shape of a token. Check it before
// hashing or looking up anything a peer presented.
func ValidToken(s string) bool {
	if len(s) != TokenLength {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil
}

// HashToken is the stored form of a bearer token: hex SHA-256. Tokens carry
// 256 bits, so an unsalted hash is enough and allows lookup by hash.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const connIDLength = 32

// NewConnID returns a random connection id: 16 bytes, lowercase hex. It
// also identifies claim attempts and web code redemptions, whose grants
// carry it as Grant.ConnID.
func NewConnID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error.
	return hex.EncodeToString(b[:])
}

// ValidConnID reports whether id has the shape of a connection id.
func ValidConnID(id string) bool {
	if len(id) != connIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
