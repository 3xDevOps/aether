package protocol

// The connector methods manage the caller's ChatGPT and Claude connectors
// (docs/connectors.md).
const (
	// MethodConnectorCode mints a one-use pairing code that authorizes a
	// connector to act as the caller, bound to the caller's edge device.
	MethodConnectorCode = "connector.code"
	// MethodConnectorList lists the caller's connectors.
	MethodConnectorList = "connector.list"
	// MethodConnectorRevoke removes one of the caller's connectors, or
	// anyone's for an admin.
	MethodConnectorRevoke = "connector.revoke"
)

// ConnectorCodeResult is the URL to add as a custom connector, the
// pairing code its consent page asks for, and when the code expires, in
// RFC 3339.
type ConnectorCodeResult struct {
	URL       string `json:"url"`
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

// Connector is one authorized connector. RedirectHost is the host its
// OAuth client returns to, such as chatgpt.com; DeviceID is the edge
// device its code was minted on, empty for none. Times are RFC 3339;
// LastUsedAt is empty until it first calls a tool.
type Connector struct {
	ID           string `json:"id"`
	MemberID     string `json:"member_id"`
	ClientName   string `json:"client_name"`
	RedirectHost string `json:"redirect_host"`
	DeviceID     string `json:"device_id,omitempty"`
	// Scopes are the scopes the member granted on the consent page: read,
	// and write and control when checked.
	Scopes     []string `json:"scopes"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt string   `json:"last_used_at,omitempty"`
}

type ConnectorListResult struct {
	Connectors []Connector `json:"connectors"`
}

type ConnectorRevokeParams struct {
	ConnectorID string `json:"connector_id"`
}

// ConnectorRevokeResult is the connector as it was before its removal.
type ConnectorRevokeResult struct {
	Connector Connector `json:"connector"`
}
