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
// edge's canonical origin; the server signs the origin it dialed instead.
type Challenge struct {
	Version int    `json:"version"`
	Nonce   []byte `json:"nonce"`
	Origin  string `json:"origin"`
}

// Hello (server to edge) answers the challenge. HostKey is the SSH wire
// form of the host public key; Signature is SignEnrollment's result.
// Version is the lower of Version and the challenge's version.
type Hello struct {
	Version      int    `json:"version"`
	HostKey      []byte `json:"host_key"`
	Signature    []byte `json:"signature"`
	AgentVersion string `json:"agent_version"`
	Name         string `json:"name"`
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
// dial. An ssh open carries a grant; a web open (TLS passthrough) has no
// account and no grant. ClientAddr is edge-asserted, for logs and rate
// limits only, never for authentication.
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

// Claim (edge to server) forwards a claim attempt. ID is the grant's
// ConnID.
type Claim struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Grant string `json:"grant"`
}

// ClaimResult (server to edge) answers a claim. An empty Error means the
// grant's account is now the server's owner.
type ClaimResult struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

// Claimed (server to edge) states that the server is claimed and by whom.
// The server sends it after a successful claim. It is informational: the
// edge records an owner only from a claim it forwarded, and a server whose
// Ready says unclaimed forgets its owner instead.
type Claimed struct {
	Owner Account `json:"owner"`
}

// Directory (server to edge) is the full list of members and live
// invitations. Each message replaces the previous one.
type Directory struct {
	Entries []DirectoryEntry `json:"entries"`
}

// WebRedeem (server to edge) redeems a web sign-in code with the PKCE
// verifier the server kept. ID is a NewConnID value and becomes the
// grant's ConnID.
type WebRedeem struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
}

// WebRedeemResult (edge to server) carries a web grant or the reason
// there is none.
type WebRedeemResult struct {
	ID    string `json:"id"`
	Grant string `json:"grant,omitempty"`
	Error string `json:"error,omitempty"`
}

// DeviceRevoked (edge to server) reports a revoked device token; the server
// closes that device's connections.
type DeviceRevoked struct {
	DeviceID string `json:"device_id"`
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

func (Challenge) controlType() string       { return "challenge" }
func (Hello) controlType() string           { return "hello" }
func (Ready) controlType() string           { return "ready" }
func (Open) controlType() string            { return "open" }
func (OpenResult) controlType() string      { return "open_result" }
func (Claim) controlType() string           { return "claim" }
func (ClaimResult) controlType() string     { return "claim_result" }
func (Claimed) controlType() string         { return "claimed" }
func (Directory) controlType() string       { return "directory" }
func (WebRedeem) controlType() string       { return "web_redeem" }
func (WebRedeemResult) controlType() string { return "web_redeem_result" }
func (DeviceRevoked) controlType() string   { return "device_revoked" }
func (Unenroll) controlType() string        { return "unenroll" }
func (Drain) controlType() string           { return "drain" }
func (Ping) controlType() string            { return "ping" }
func (Pong) controlType() string            { return "pong" }

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
	return nil
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
	case m.Kind == KindSSH && (m.Grant == "" || len(m.Grant) > MaxGrantSize):
		return errors.New("ssh open needs a grant")
	case m.Kind == KindWeb && m.Grant != "":
		return errors.New("web open carries no grant")
	case m.Kind != KindSSH && m.Kind != KindWeb:
		return fmt.Errorf("unknown open kind %q", m.Kind)
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

func (m Claim) validate() error {
	if !ValidConnID(m.ID) {
		return fmt.Errorf("invalid claim id %q", m.ID)
	}
	if _, _, err := ParseClaimCode(m.Code); err != nil {
		return err
	}
	if m.Grant == "" || len(m.Grant) > MaxGrantSize {
		return errors.New("claim needs a grant")
	}
	return nil
}

func (m ClaimResult) validate() error {
	return validateReply(m.ID, m.Error)
}

func (m Claimed) validate() error {
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

func (m WebRedeem) validate() error {
	switch {
	case !ValidConnID(m.ID):
		return fmt.Errorf("invalid redeem id %q", m.ID)
	case !ValidToken(m.Code):
		return errors.New("invalid web sign-in code")
	case !ValidToken(m.Verifier):
		return errors.New("invalid verifier")
	}
	return nil
}

func (m WebRedeemResult) validate() error {
	if (m.Grant == "") == (m.Error == "") {
		return errors.New("web redeem result carries exactly one of grant or error")
	}
	if len(m.Grant) > MaxGrantSize {
		return errors.New("grant too large")
	}
	return validateReply(m.ID, m.Error)
}

func (m DeviceRevoked) validate() error {
	if !validRequiredText(m.DeviceID, maxIDText) {
		return errors.New("invalid device id")
	}
	return nil
}

func (Unenroll) validate() error { return nil }
func (Drain) validate() error    { return nil }
func (Ping) validate() error     { return nil }
func (Pong) validate() error     { return nil }

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
	case "claim":
		return decodeAs[Claim](data)
	case "claim_result":
		return decodeAs[ClaimResult](data)
	case "claimed":
		return decodeAs[Claimed](data)
	case "directory":
		return decodeAs[Directory](data)
	case "web_redeem":
		return decodeAs[WebRedeem](data)
	case "web_redeem_result":
		return decodeAs[WebRedeemResult](data)
	case "device_revoked":
		return decodeAs[DeviceRevoked](data)
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
