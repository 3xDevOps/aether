package protocol

// MethodServerEdge reports the server's edge to any member.
const MethodServerEdge = "server.edge"

// ServerEdgeResult describes the server's edge. EdgeURL is empty when the
// server is not enrolled with an edge; ServerID is set either way. Owner
// is nil while the server has no owner at its edge.
type ServerEdgeResult struct {
	EdgeURL      string       `json:"edge_url"`
	ServerID     string       `json:"server_id"`
	AccessPolicy string       `json:"access_policy"`
	Owner        *EdgeAccount `json:"owner"`
	Connected    bool         `json:"connected"`
}

// EdgeAccount is an account signed in at an edge.
type EdgeAccount struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
	Login    string `json:"login"`
	Email    string `json:"email"`
}
