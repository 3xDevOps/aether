package edgeproto

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/crypto/ssh"
)

// MaxControlMessageSize bounds one control-channel message. Readers set it
// as the WebSocket read limit.
const MaxControlMessageSize = 1 << 20

// MaxDirectoryEntries bounds one directory message.
const MaxDirectoryEntries = 1000

const maxHostKeySize = 16 << 10

// ErrUnknownMessage wraps the error DecodeControl returns for a message type
// this build does not know. A reader may skip such a message: a newer peer
// sent it.
var ErrUnknownMessage = errors.New("unknown control message type")

// Message is one control-channel message. The concrete types below are the
// only implementations; DecodeControl returns them as values.
type Message interface {
	controlType() string
	validate() error
}

// Server states in Ready.
const (
	StateUnclaimed = "unclaimed"
	StateClaimed   = "claimed"
)

// Challenge (edge to server) opens the control channel. Origin is the
// edge's canonical relay origin; the server signs the origin it dialed
// instead.
type Challenge struct {
	Version int    `json:"version"`
	Nonce   []byte `json:"nonce"`
	Origin  string `json:"origin"`
}

// Hello (server to edge) answers the challenge. HostKey is the SSH wire
// form of the host public key; Signature is SignEnrollment's result.
// Version is the lower of Version and the challenge's version.
// AccessPolicy is the server's policy, read with ParseAccessPolicy; the
// signature does not cover it, and the edge only reports it. A server
// announces a changed policy by enrolling again.
type Hello struct {
	Version      int          `json:"version"`
	HostKey      []byte       `json:"host_key"`
	Signature    []byte       `json:"signature"`
	AgentVersion string       `json:"agent_version"`
	Name         string       `json:"name"`
	AccessPolicy AccessPolicy `json:"access_policy"`
}

// PublicKey parses HostKey.
func (h Hello) PublicKey() (ssh.PublicKey, error) {
	key, err := ssh.ParsePublicKey(h.HostKey)
	if err != nil {
		return nil, fmt.Errorf("edgeproto: parse host key: %w", err)
	}
	return key, nil
}

// Ready (edge to server) confirms enrollment. EdgeKey is the key grants are
// signed with; the server pins it.
type Ready struct {
	ServerID string            `json:"server_id"`
	State    string            `json:"state"`
	EdgeKey  ed25519.PublicKey `json:"edge_key"`
}

// Open (edge to server) asks the server to attach a data socket for one
// client connection, with Ticket as the bearer token of the data socket
// dial, and a grant of the same Kind. ClientAddr is edge-asserted, for
// logs and rate limits only, never for authentication.
type Open struct {
	ConnID     string `json:"conn_id"`
	Ticket     string `json:"ticket"`
	Kind       string `json:"kind"`
	Grant      string `json:"grant,omitempty"`
	ClientAddr string `json:"client_addr,omitempty"`
}

// OpenResult (server to edge) answers every open. An empty Error means the
// server is dialing the data socket; otherwise the edge refuses the client
// with Error.
type OpenResult struct {
	ConnID string `json:"conn_id"`
	Error  string `json:"error,omitempty"`
}

// Claimed (server to edge) states that the claim connection ConnID
// presented the server's claim code, and that Owner, the account of that
// connection's grant, is now the server's owner. The edge records Owner
// only when it opened ConnID as a claim for that account.
type Claimed struct {
	ConnID string    `json:"conn_id"`
	Owner  Principal `json:"owner"`
}

// OwnerTransferred (server to edge) states that an admin transferred the
// server's ownership to Owner, or, sent at enrollment, repeats the
// server's owner. ID, from NewConnID, names this report.
type OwnerTransferred struct {
	ID    string    `json:"id"`
	Owner Principal `json:"owner"`
}

// OwnerTransferResult (edge to server) answers the OwnerTransferred with
// the same ID. An empty Error means the edge recorded Owner; otherwise the
// edge kept the owner it had, and Error says why.
type OwnerTransferResult struct {
	ID    string    `json:"id"`
	Owner Principal `json:"owner"`
	Error string    `json:"error,omitempty"`
}

// Ownerless (server to edge) states that the server has no owner. It stays
// enrolled; a new claim code from its host gives it one again.
type Ownerless struct{}

// Directory (server to edge) is the full list of members and live
// invitations. Each message replaces the previous one.
type Directory struct {
	Entries []DirectoryEntry `json:"entries"`
}

// DeviceRevoked (edge to server) reports a revoked device token; the server
// closes that device's connections.
type DeviceRevoked struct {
	DeviceID string `json:"device_id"`
}

// AccountDeleted (edge to server) reports that the account Provider and
// Subject was deleted at the edge. The server removes that identity and
// its edge devices, and nothing else.
type AccountDeleted struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
}

// AccountDeletionApplied (server to edge) answers AccountDeleted once the
// server removed that identity and its edge devices, or holds no such
// identity. The edge sends the deletion again at each enrollment until
// this answer arrives.
type AccountDeletionApplied struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
}

// Unenroll, from the edge, reports that the owner removed the server at the
// edge; from the server, that its operator ran `aether-server edge leave`.
// The receiver forgets the enrollment and the control channel closes.
type Unenroll struct{}

// Drain (edge to server) announces a clean edge stop; the server
// reconnects with backoff.
type Drain struct{}

// Ping and Pong keep the control channel alive: see PingInterval and
// ControlIdleTimeout.
type (
	Ping struct{}
	Pong struct{}
)

func (Challenge) controlType() string              { return "challenge" }
func (Hello) controlType() string                  { return "hello" }
func (Ready) controlType() string                  { return "ready" }
func (Open) controlType() string                   { return "open" }
func (OpenResult) controlType() string             { return "open_result" }
func (Claimed) controlType() string                { return "claimed" }
func (OwnerTransferred) controlType() string       { return "owner_transferred" }
func (OwnerTransferResult) controlType() string    { return "owner_transfer_result" }
func (Ownerless) controlType() string              { return "ownerless" }
func (Directory) controlType() string              { return "directory" }
func (DeviceRevoked) controlType() string          { return "device_revoked" }
func (AccountDeleted) controlType() string         { return "account_deleted" }
func (AccountDeletionApplied) controlType() string { return "account_deletion_applied" }
func (Unenroll) controlType() string               { return "unenroll" }
func (Drain) controlType() string                  { return "drain" }
func (Ping) controlType() string                   { return "ping" }
func (Pong) controlType() string                   { return "pong" }

func (m Challenge) validate() error {
	if len(m.Nonce) != NonceSize {
		return fmt.Errorf("nonce is %d bytes, want %d", len(m.Nonce), NonceSize)
	}
	if o, err := Origin(m.Origin); err != nil || o != m.Origin {
		return fmt.Errorf("origin %q is not a canonical edge origin", m.Origin)
	}
	return nil
}

func (m Hello) validate() error {
	if len(m.HostKey) > maxHostKeySize || len(m.Signature) > maxHostKeySize {
		return errors.New("host key or signature too large")
	}
	key, err := m.PublicKey()
	if err != nil {
		return err
	}
	if _, ok := key.(*ssh.Certificate); ok {
		return errors.New("host key is a certificate")
	}
	if !validText(m.AgentVersion, maxIDText) || !validText(m.Name, maxIDText) {
		return errors.New("agent version or name is too long or has control characters")
	}
	_, err = ParseAccessPolicy(string(m.AccessPolicy))
	return err
}

func (m Ready) validate() error {
	switch {
	case !ValidServerID(m.ServerID):
		return fmt.Errorf("invalid server id %q", m.ServerID)
	case m.State != StateUnclaimed && m.State != StateClaimed:
		return fmt.Errorf("unknown server state %q", m.State)
	case len(m.EdgeKey) != ed25519.PublicKeySize:
		return fmt.Errorf("edge key is %d bytes, want %d", len(m.EdgeKey), ed25519.PublicKeySize)
	}
	return nil
}

func (m Open) validate() error {
	switch {
	case !ValidConnID(m.ConnID):
		return fmt.Errorf("invalid conn id %q", m.ConnID)
	case !ValidToken(m.Ticket):
		return errors.New("invalid ticket")
	case m.Kind != KindSSH && m.Kind != KindClaim:
		return fmt.Errorf("unknown open kind %q", m.Kind)
	case m.Grant == "" || len(m.Grant) > MaxGrantSize:
		return errors.New("open needs a grant")
	}
	if m.ClientAddr != "" {
		if _, err := netip.ParseAddrPort(m.ClientAddr); err != nil {
			return fmt.Errorf("client address: %w", err)
		}
	}
	return nil
}

func (m OpenResult) validate() error {
	return validateReply(m.ConnID, m.Error)
}

func (m Claimed) validate() error {
	if !ValidConnID(m.ConnID) {
		return fmt.Errorf("invalid conn id %q", m.ConnID)
	}
	if m.Owner.Type != PrincipalAccount {
		return fmt.Errorf("a claim makes an account the owner, not a %q", m.Owner.Type)
	}
	return m.Owner.Validate()
}

func (m OwnerTransferred) validate() error {
	if !ValidConnID(m.ID) {
		return fmt.Errorf("invalid id %q", m.ID)
	}
	return m.Owner.Validate()
}

func (m OwnerTransferResult) validate() error {
	if err := validateReply(m.ID, m.Error); err != nil {
		return err
	}
	return m.Owner.Validate()
}

func (m Directory) validate() error {
	if len(m.Entries) > MaxDirectoryEntries {
		return fmt.Errorf("directory has %d entries, limit %d", len(m.Entries), MaxDirectoryEntries)
	}
	for i, e := range m.Entries {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
	}
	return nil
}

func (m DeviceRevoked) validate() error {
	if !validRequiredText(m.DeviceID, maxIDText) {
		return errors.New("invalid device id")
	}
	return nil
}

func (m AccountDeleted) validate() error {
	if !validRequiredText(m.Subject, maxShortText) {
		return errors.New("an account is named by a provider and a subject")
	}
	return CheckProvider(m.Provider)
}

func (m AccountDeletionApplied) validate() error {
	return AccountDeleted(m).validate()
}

func (Ownerless) validate() error { return nil }
func (Unenroll) validate() error  { return nil }
func (Drain) validate() error     { return nil }
func (Ping) validate() error      { return nil }
func (Pong) validate() error      { return nil }

func validateReply(id, errText string) error {
	if !ValidConnID(id) {
		return fmt.Errorf("invalid id %q", id)
	}
	if !validText(errText, maxErrorText) {
		return errors.New("error text is too long or has control characters")
	}
	return nil
}

// EncodeControl validates m and encodes it as one JSON object whose
// "type" member names the message.
func EncodeControl(m Message) ([]byte, error) {
	t := m.controlType()
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("edgeproto: encode %s: %w", t, err)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("edgeproto: encode %s: %w", t, err)
	}
	out := make([]byte, 0, len(body)+len(t)+11)
	out = append(out, `{"type":"`...)
	out = append(out, t...)
	out = append(out, '"')
	if len(body) > len("{}") {
		out = append(out, ',')
	}
	out = append(out, body[1:]...)
	if len(out) > MaxControlMessageSize {
		return nil, fmt.Errorf("edgeproto: %s message is %d bytes, limit %d", t, len(out), MaxControlMessageSize)
	}
	return out, nil
}

// DecodeControl decodes and validates one control message.
func DecodeControl(data []byte) (Message, error) {
	if len(data) > MaxControlMessageSize {
		return nil, fmt.Errorf("edgeproto: control message is %d bytes, limit %d", len(data), MaxControlMessageSize)
	}
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("edgeproto: decode control message: %w", err)
	}
	switch env.Type {
	case "challenge":
		return decodeAs[Challenge](data)
	case "hello":
		return decodeAs[Hello](data)
	case "ready":
		return decodeAs[Ready](data)
	case "open":
		return decodeAs[Open](data)
	case "open_result":
		return decodeAs[OpenResult](data)
	case "claimed":
		return decodeAs[Claimed](data)
	case "owner_transferred":
		return decodeAs[OwnerTransferred](data)
	case "owner_transfer_result":
		return decodeAs[OwnerTransferResult](data)
	case "ownerless":
		return decodeAs[Ownerless](data)
	case "directory":
		return decodeAs[Directory](data)
	case "device_revoked":
		return decodeAs[DeviceRevoked](data)
	case "account_deleted":
		return decodeAs[AccountDeleted](data)
	case "account_deletion_applied":
		return decodeAs[AccountDeletionApplied](data)
	case "unenroll":
		return decodeAs[Unenroll](data)
	case "drain":
		return decodeAs[Drain](data)
	case "ping":
		return decodeAs[Ping](data)
	case "pong":
		return decodeAs[Pong](data)
	}
	return nil, fmt.Errorf("edgeproto: %w %q", ErrUnknownMessage, env.Type)
}

func decodeAs[T Message](data []byte) (Message, error) {
	var m T
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("edgeproto: decode %s: %w", m.controlType(), err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("edgeproto: invalid %s: %w", m.controlType(), err)
	}
	return m, nil
}
