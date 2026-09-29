package edgeproto

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Sign-in providers. An account is keyed by (provider, subject); nothing
// merges two accounts by email.
const (
	ProviderGitHub = "github"
	ProviderGoogle = "google"
)

// ValidProvider reports whether p is a supported sign-in provider.
func ValidProvider(p string) bool {
	return p == ProviderGitHub || p == ProviderGoogle
}

// Account is a person signed in to the edge. Subject is the provider's
// immutable user id. Email is set only when the provider verified it.
// Login is the GitHub login; Google accounts have none. IdentityAt is
// when the provider last reported Email and Login: the account's last
// browser sign-in at the edge.
type Account struct {
	Provider   string    `json:"provider"`
	Subject    string    `json:"subject"`
	Email      string    `json:"email,omitempty"`
	Login      string    `json:"login,omitempty"`
	Name       string    `json:"name,omitempty"`
	IdentityAt time.Time `json:"identity_at,omitzero"`
}

// IdentityCurrent reports whether a's Email and Login are at most
// IdentityMaxAge old at now.
func (a Account) IdentityCurrent(now time.Time) bool {
	return !a.IdentityAt.IsZero() && now.Sub(a.IdentityAt) <= IdentityMaxAge
}

// Validate checks an account's shape. Provider data that fails it, such as
// a display name with control characters, must be cleaned by the edge
// before it builds the Account.
func (a Account) Validate() error {
	switch {
	case !ValidProvider(a.Provider):
		return fmt.Errorf("edgeproto: unknown provider %q", a.Provider)
	case !validRequiredText(a.Subject, maxShortText):
		return errors.New("edgeproto: account subject is empty, too long or has control characters")
	case !validText(a.Email, maxEmail) || (a.Email != "" && !strings.Contains(a.Email, "@")):
		return fmt.Errorf("edgeproto: invalid account email %q", a.Email)
	case a.Login != "" && a.Provider != ProviderGitHub:
		return fmt.Errorf("edgeproto: %s account has a login", a.Provider)
	case !validText(a.Login, maxIDText):
		return errors.New("edgeproto: account login is too long or has control characters")
	case !validText(a.Name, maxShortText):
		return errors.New("edgeproto: account name is too long or has control characters")
	}
	return nil
}

// accountIDPrefix sets account ids apart from server ids, which are the
// same length and alphabet.
const accountIDPrefix = "acct_"

// accountIDLength is accountIDPrefix and 26 base32 characters, 128 bits.
const accountIDLength = len(accountIDPrefix) + 26

// NewAccountID returns a fresh internal account id from crypto/rand.
func NewAccountID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error.
	return accountIDPrefix + strings.ToLower(base32NoPad.EncodeToString(b[:]))
}

// ValidAccountID reports whether id has the shape of an account id.
func ValidAccountID(id string) bool {
	rest, ok := strings.CutPrefix(id, accountIDPrefix)
	return ok && len(id) == accountIDLength && isLowerBase32(rest)
}

// AccountInfo is an account as the edge's API names it to that account's
// clients: ID, the internal id the edge assigned it, and the provider
// identity. Grants and control messages carry the provider identity
// without ID, because servers key members on (provider, subject) so that
// a later change of what an account id covers, such as linking a second
// provider, cannot change who a member is on any server.
type AccountInfo struct {
	ID string `json:"id"`
	Account
}

// Validate checks the id and the account.
func (a AccountInfo) Validate() error {
	if !ValidAccountID(a.ID) {
		return fmt.Errorf("edgeproto: invalid account id %q", a.ID)
	}
	return a.Account.Validate()
}

// PrincipalType names what kind of party owns a server.
type PrincipalType string

const (
	PrincipalAccount PrincipalType = "account"
	// PrincipalTeam is reserved for team-owned servers. No team has an
	// identifier yet, so Principal.Validate refuses it.
	PrincipalTeam PrincipalType = "team"
)

// Principal is the owner of a server. An account principal is named by
// Provider and Subject, the key servers store.
type Principal struct {
	Type     PrincipalType `json:"type"`
	Provider string        `json:"provider,omitempty"`
	Subject  string        `json:"subject,omitempty"`
}

// AccountPrincipal is the principal of account a.
func AccountPrincipal(a Account) Principal {
	return Principal{Type: PrincipalAccount, Provider: a.Provider, Subject: a.Subject}
}

// Validate checks a principal's shape.
func (p Principal) Validate() error {
	switch p.Type {
	case PrincipalAccount:
		if !ValidProvider(p.Provider) || !validRequiredText(p.Subject, maxShortText) {
			return errors.New("edgeproto: an account principal has a provider and a subject")
		}
		return nil
	case PrincipalTeam:
		return errors.New("edgeproto: team principals are reserved and not supported by this version")
	}
	return fmt.Errorf("edgeproto: unknown principal type %q", p.Type)
}

// Device is one client install with its own device token at the edge. Key
// is the device's public key as an authorized_keys line.
type Device struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Key   string `json:"key"`
}

// DeviceStatus is where a device stands at a server, as servers report it
// to clients.
type DeviceStatus string

const (
	// DeviceStatusRegistered is a device admitted under PolicyAccount on
	// first connection, never approved by a person. Under
	// PolicyApprovedDevices it is refused until someone approves it.
	DeviceStatusRegistered DeviceStatus = "registered"
	DeviceStatusApproved   DeviceStatus = "approved"
	DeviceStatusPending    DeviceStatus = "pending"
	DeviceStatusRevoked    DeviceStatus = "revoked"
)

// ParseDeviceStatus returns the status s names, and refuses any other
// value, the empty one included.
func ParseDeviceStatus(s string) (DeviceStatus, error) {
	switch d := DeviceStatus(s); d {
	case DeviceStatusRegistered, DeviceStatusApproved, DeviceStatusPending, DeviceStatusRevoked:
		return d, nil
	}
	return "", fmt.Errorf("edgeproto: unknown device status %q", s)
}

// ParseDeviceKey parses a device key: one Ed25519 authorized_keys line with
// no options.
func ParseDeviceKey(line string) (ssh.PublicKey, error) {
	if len(line) > maxShortText {
		return nil, errors.New("edgeproto: device key is too long")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("edgeproto: parse device key: %w", err)
	}
	if len(options) != 0 || len(rest) != 0 {
		return nil, errors.New("edgeproto: device key must be a single authorized_keys line without options")
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("edgeproto: device key is %s, want %s", key.Type(), ssh.KeyAlgoED25519)
	}
	return key, nil
}

// DeviceKeyLine is the canonical authorized_keys form of a device key,
// without a comment.
func DeviceKeyLine(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// DeviceKeyMatches reports whether offered is the device key a grant names.
func DeviceKeyMatches(grantKey string, offered ssh.PublicKey) bool {
	want, err := ParseDeviceKey(grantKey)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(want.Marshal(), offered.Marshal()) == 1
}

// Directory entry kinds.
const (
	EntryMember     = "member"
	EntryInvitation = "invitation"
)

// DirectoryEntry is one line of the server's directory at the edge. A
// member is named by Provider and Subject. An invitation names either a
// GitHub Login (Provider "github") or an Email; an email invitation with an
// empty Provider matches a verified email from either provider.
// ExpiresAt is set for invitations only.
type DirectoryEntry struct {
	Kind      string    `json:"kind"`
	Provider  string    `json:"provider,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	Login     string    `json:"login,omitempty"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Validate checks an entry's shape.
func (e DirectoryEntry) Validate() error {
	if !validRequiredText(e.Role, maxIDText) {
		return fmt.Errorf("edgeproto: invalid directory role %q", e.Role)
	}
	switch e.Kind {
	case EntryMember:
		if !ValidProvider(e.Provider) || !validRequiredText(e.Subject, maxShortText) ||
			e.Login != "" || e.Email != "" || !e.ExpiresAt.IsZero() {
			return errors.New("edgeproto: a member entry has exactly a provider, a subject and a role")
		}
	case EntryInvitation:
		if e.Subject != "" || e.ExpiresAt.IsZero() {
			return errors.New("edgeproto: an invitation entry has an expiry and no subject")
		}
		switch {
		case e.Login != "" && e.Email == "":
			if e.Provider != ProviderGitHub || !validText(e.Login, maxIDText) {
				return fmt.Errorf("edgeproto: invalid login invitation %q", e.Login)
			}
		case e.Email != "" && e.Login == "":
			if (e.Provider != "" && !ValidProvider(e.Provider)) || !validText(e.Email, maxEmail) ||
				!strings.Contains(e.Email, "@") {
				return fmt.Errorf("edgeproto: invalid email invitation %q", e.Email)
			}
		default:
			return errors.New("edgeproto: an invitation names exactly one of login or email")
		}
	default:
		return fmt.Errorf("edgeproto: unknown directory entry kind %q", e.Kind)
	}
	return nil
}

// Matches reports whether a signed-in account is this entry at now.
// Members match by provider and subject only. Invitations match a GitHub
// login or a verified email, case-insensitively in ASCII only, so that
// Unicode case folding (the Kelvin sign folds to "k") cannot widen a
// match, and only while a.IdentityCurrent: an account that has not
// signed in since its login or email moved to someone else still holds
// the old one. An expired invitation matches nothing.
func (e DirectoryEntry) Matches(a Account, now time.Time) bool {
	switch e.Kind {
	case EntryMember:
		return e.Subject != "" && e.Provider == a.Provider && e.Subject == a.Subject
	case EntryInvitation:
		if !now.Before(e.ExpiresAt) || !a.IdentityCurrent(now) {
			return false
		}
		if e.Login != "" {
			return e.Provider == ProviderGitHub && a.Provider == ProviderGitHub && asciiEqualFold(e.Login, a.Login)
		}
		if e.Email != "" {
			return (e.Provider == "" || e.Provider == a.Provider) && asciiEqualFold(e.Email, a.Email)
		}
	}
	return false
}

func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if asciiLower(a[i]) != asciiLower(b[i]) {
			return false
		}
	}
	return true
}

func asciiLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
