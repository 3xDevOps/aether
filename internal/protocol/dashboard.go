package protocol

// Dashboard gateway methods. The HTTP/WS transport has no login of its
// own: bearer tokens are minted and revoked over the authenticated SSH
// control channel, so the SSH key stays the only identity system.
const (
	MethodDashTokenMint   = "dash.token.mint"
	MethodDashTokenRevoke = "dash.token.revoke"
)

// DashTokenMintParams are the params of dash.token.mint. TTLSeconds is
// capped at the gateway's maximum and has no floor; zero means the
// gateway default.
type DashTokenMintParams struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// DashTokenMintResult is the result of dash.token.mint. URL is set only
// when the server exposes the dashboard directly (--dashboard-addr);
// clients reaching the gateway through an SSH forward build their own
// loopback URL from the forwarded port.
type DashTokenMintResult struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	URL       string `json:"url,omitempty"`
}

// DashTokenRevokeParams are the params of dash.token.revoke; a member may
// revoke only tokens minted for itself.
type DashTokenRevokeParams struct {
	Token string `json:"token"`
}

// DashAttachRequest is the header frame of /ws/attach/{run}; the run
// comes from the path. Write access is opt-in - the zero value is the
// read-only mirror the dashboard defaults to - and is refused unless the
// member holds the steer capability on that run. The ack is the shared
// AttachResponse.
type DashAttachRequest struct {
	Write bool `json:"write,omitempty"`
	Cols  uint `json:"cols,omitempty"`
	Rows  uint `json:"rows,omitempty"`
}

// Control frame kinds a client sends on /ws/attach/{run}.
const (
	DashAttachInput  = "input"
	DashAttachResize = "resize"
)

// DashAttachControl is one client control frame on /ws/attach/{run}:
// {"type":"input","data":"ls\r"} or {"type":"resize","cols":120,"rows":40}.
// Terminal output travels the other way as binary frames.
type DashAttachControl struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint   `json:"cols,omitempty"`
	Rows uint   `json:"rows,omitempty"`
}
