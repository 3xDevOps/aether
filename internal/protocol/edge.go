package protocol

// MethodServerEdge reports the server's edge to any member.
const MethodServerEdge = "server.edge"

// ServerEdgeResult describes the server's edge. EdgeURL is empty when the
// server is not enrolled with an edge; ServerID is set either way. Owner
// is nil while the server has no owner at its edge. Claimed is whether
// the edge last reported the server as claimed.
type ServerEdgeResult struct {
	EdgeURL      string       `json:"edge_url"`
	ServerID     string       `json:"server_id"`
	AccessPolicy string       `json:"access_policy"`
	Owner        *EdgeAccount `json:"owner"`
	Connected    bool         `json:"connected"`
	Claimed      bool         `json:"claimed"`
}

// EdgeAccount is an account signed in at an edge.
type EdgeAccount struct {
	Provider string `json:"provider"`
	Login    string `json:"login"`
}

// MethodServerEdgeClaimCode issues the calling admin a claim code that
// binds the account claiming with it to that admin's member.
const MethodServerEdgeClaimCode = "server.edge.claimcode"

// ServerEdgeClaimCodeResult is a claim code and when it expires, in
// RFC 3339.
type ServerEdgeClaimCodeResult struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}
