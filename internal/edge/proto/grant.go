package edgeproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Grant and open kinds. An ssh grant authorizes one relayed SSH connection
// by a member; a claim grant one relayed SSH connection, to a server
// without an owner, that presents a claim code in its SSH user name (see
// ClaimUser).
const (
	KindSSH   = "ssh"
	KindClaim = "claim"
)

// GrantTTL is the longest lifetime of a grant. ClockSkew is the tolerance
// applied to both ends of that lifetime.
const (
	GrantTTL  = 60 * time.Second
	ClockSkew = 30 * time.Second
)

// MaxGrantSize bounds an encoded grant.
const MaxGrantSize = 8 << 10

const grantContext = "aether-edge-grant-v1\x00"

// Grant is the edge's signed statement that Account, on device DeviceID
// with DeviceKey, its authorized_keys line, is opening the connection
// ConnID of Kind to ServerID. Issuer is the edge's canonical relay origin,
// the origin the server enrolled with.
type Grant struct {
	Issuer      string    `json:"issuer"`
	ServerID    string    `json:"server_id"`
	ConnID      string    `json:"conn_id"`
	Kind        string    `json:"kind"`
	Account     Account   `json:"account"`
	DeviceID    string    `json:"device_id"`
	DeviceKey   string    `json:"device_key"`
	DeviceLabel string    `json:"device_label,omitempty"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Grant verification errors. VerifyGrant wraps them with detail; test with
// errors.Is.
var (
	ErrGrantMalformed   = errors.New("grant is malformed")
	ErrGrantSignature   = errors.New("grant signature is not from the pinned edge key")
	ErrGrantIssuer      = errors.New("grant is from another edge origin")
	ErrGrantServer      = errors.New("grant is for another server")
	ErrGrantConn        = errors.New("grant is for another connection")
	ErrGrantKind        = errors.New("grant is for another kind of connection")
	ErrGrantExpired     = errors.New("grant expired")
	ErrGrantNotYetValid = errors.New("grant is not yet valid")
)

func (g Grant) validate() error {
	if o, err := Origin(g.Issuer); err != nil || o != g.Issuer {
		return fmt.Errorf("issuer %q is not a canonical edge origin", g.Issuer)
	}
	switch {
	case g.Kind != KindSSH && g.Kind != KindClaim:
		return fmt.Errorf("unknown grant kind %q", g.Kind)
	case !ValidServerID(g.ServerID):
		return fmt.Errorf("invalid server id %q", g.ServerID)
	case !ValidConnID(g.ConnID):
		return fmt.Errorf("invalid conn id %q", g.ConnID)
	case !validRequiredText(g.DeviceID, maxIDText):
		return errors.New("invalid device id")
	case !validText(g.DeviceLabel, maxShortText):
		return errors.New("device label is too long or has control characters")
	case g.IssuedAt.IsZero() || !g.ExpiresAt.After(g.IssuedAt):
		return errors.New("grant must expire after it is issued")
	case g.ExpiresAt.Sub(g.IssuedAt) > GrantTTL:
		return fmt.Errorf("grant lifetime %s exceeds %s", g.ExpiresAt.Sub(g.IssuedAt), GrantTTL)
	}
	if err := g.Account.Validate(); err != nil {
		return err
	}
	_, err := ParseDeviceKey(g.DeviceKey)
	return err
}

// SignGrant encodes and signs g with the edge key. The result is
// base64url(payload) "." base64url(signature), where payload is the JSON
// of g and the signature covers "aether-edge-grant-v1\x00" || payload.
func SignGrant(edgeKey ed25519.PrivateKey, g Grant) (string, error) {
	g.IssuedAt = g.IssuedAt.UTC()
	g.ExpiresAt = g.ExpiresAt.UTC()
	if err := g.validate(); err != nil {
		return "", fmt.Errorf("edgeproto: sign grant: %w", err)
	}
	payload, err := json.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("edgeproto: encode grant: %w", err)
	}
	sig := ed25519.Sign(edgeKey, grantMessage(payload))
	signed := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	if len(signed) > MaxGrantSize {
		return "", fmt.Errorf("edgeproto: signed grant is %d bytes, limit %d", len(signed), MaxGrantSize)
	}
	return signed, nil
}

func grantMessage(payload []byte) []byte {
	return append([]byte(grantContext), payload...)
}

// GrantScope is what the verifier expects a grant to be for. Issuer is the
// canonical origin of the edge URL the server enrolled with.
type GrantScope struct {
	Issuer   string
	ServerID string
	ConnID   string
	Kind     string
}

// VerifyGrant checks signed against the pinned edge key, then its scope and
// validity window at now, allowing ClockSkew at both ends. The signature is
// checked before the payload is parsed.
func VerifyGrant(edgeKey ed25519.PublicKey, signed string, want GrantScope, now time.Time) (Grant, error) {
	if len(edgeKey) != ed25519.PublicKeySize {
		return Grant{}, fmt.Errorf("edgeproto: edge key is %d bytes, want %d", len(edgeKey), ed25519.PublicKeySize)
	}
	if len(signed) > MaxGrantSize {
		return Grant{}, fmt.Errorf("%w: %d bytes, limit %d", ErrGrantMalformed, len(signed), MaxGrantSize)
	}
	encPayload, encSig, ok := strings.Cut(signed, ".")
	if !ok {
		return Grant{}, fmt.Errorf("%w: no signature", ErrGrantMalformed)
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encPayload)
	if err != nil {
		return Grant{}, fmt.Errorf("%w: payload: %v", ErrGrantMalformed, err)
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(encSig)
	if err != nil {
		return Grant{}, fmt.Errorf("%w: signature: %v", ErrGrantMalformed, err)
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(edgeKey, grantMessage(payload), sig) {
		return Grant{}, ErrGrantSignature
	}
	var g Grant
	if err := json.Unmarshal(payload, &g); err != nil {
		return Grant{}, fmt.Errorf("%w: %v", ErrGrantMalformed, err)
	}
	if err := g.validate(); err != nil {
		return Grant{}, fmt.Errorf("%w: %v", ErrGrantMalformed, err)
	}
	switch {
	case g.Issuer != want.Issuer:
		return Grant{}, fmt.Errorf("%w: grant names %s, this server enrolled with %s", ErrGrantIssuer, g.Issuer, want.Issuer)
	case g.ServerID != want.ServerID:
		return Grant{}, fmt.Errorf("%w: grant names %s, this server is %s", ErrGrantServer, g.ServerID, want.ServerID)
	case g.ConnID != want.ConnID:
		return Grant{}, fmt.Errorf("%w: grant names %s, want %s", ErrGrantConn, g.ConnID, want.ConnID)
	case g.Kind != want.Kind:
		return Grant{}, fmt.Errorf("%w: grant is %s, want %s", ErrGrantKind, g.Kind, want.Kind)
	case now.Before(g.IssuedAt.Add(-ClockSkew)):
		return Grant{}, fmt.Errorf("%w: issued at %s, now %s", ErrGrantNotYetValid, g.IssuedAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	case now.After(g.ExpiresAt.Add(ClockSkew)):
		return Grant{}, fmt.Errorf("%w: at %s, now %s", ErrGrantExpired, g.ExpiresAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return g, nil
}

// EdgeKeyFingerprint formats an edge public key the way ssh-keygen -l
// does: "SHA256:" and the unpadded base64 of the key's SSH wire form.
func EdgeKeyFingerprint(key ed25519.PublicKey) string {
	wire := ssh.Marshal(struct {
		Algo string
		Key  []byte
	}{ssh.KeyAlgoED25519, key})
	sum := sha256.Sum256(wire)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}
