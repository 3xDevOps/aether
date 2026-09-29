package edgeproto

import "fmt"

// AccessPolicy is a server's rule for what a grant is worth. The server
// sets it on its own host and enforces it; the edge only reports it.
type AccessPolicy string

const (
	// PolicyAccount admits a signed-in member's new device on first
	// connection.
	PolicyAccount AccessPolicy = "account"
	// PolicyApprovedDevices admits a new device only once a person
	// approved it.
	PolicyApprovedDevices AccessPolicy = "approved-devices"
)

// ParseAccessPolicy returns the policy s names. An empty s is
// PolicyApprovedDevices, so a hello, a servers list or a configuration
// that names no policy gets the stricter one. Any other value is refused.
func ParseAccessPolicy(s string) (AccessPolicy, error) {
	switch p := AccessPolicy(s); p {
	case "":
		return PolicyApprovedDevices, nil
	case PolicyAccount, PolicyApprovedDevices:
		return p, nil
	}
	return "", fmt.Errorf("edgeproto: unknown access policy %q, want %s or %s", s, PolicyAccount, PolicyApprovedDevices)
}
