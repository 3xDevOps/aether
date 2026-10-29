package protocol

// MethodAuditList lists audit history, newest first: the caller's own
// entries, or every member's for an admin (docs/connectors.md#audit-history).
const MethodAuditList = "audit.list"

// AuditListMaxLimit bounds one page of audit.list; AuditListDefaultLimit
// is a page when limit is 0.
const (
	AuditListDefaultLimit = 50
	AuditListMaxLimit     = 200
)

// AuditListParams narrows audit.list to one connector's entries when
// ConnectorID is set. Before is a previous page's next_before.
type AuditListParams struct {
	ConnectorID string `json:"connector_id,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Before      int64  `json:"before,omitempty"`
}

// AuditEntry is one audited call. Via is dashboard, cli, connector or
// assistant; ConnectorID and Client name the connector, such as ChatGPT,
// or the Slack link.
// Target is the run, swarm, connector or member acted on. Outcome is ok,
// or error with the protocol error code in Code.
type AuditEntry struct {
	Seq         int64  `json:"seq"`
	At          string `json:"at"`
	MemberID    string `json:"member_id"`
	Via         string `json:"via"`
	ConnectorID string `json:"connector_id,omitempty"`
	Client      string `json:"client,omitempty"`
	Method      string `json:"method"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Target      string `json:"target,omitempty"`
	Outcome     string `json:"outcome"`
	Code        int    `json:"code,omitempty"`
}

// AuditListResult is one page; NextBefore is set when older entries may
// follow.
type AuditListResult struct {
	Entries    []AuditEntry `json:"entries"`
	NextBefore int64        `json:"next_before,omitempty"`
}
