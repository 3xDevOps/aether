package protocol

// SubsystemRunSSH carries one SSH connection into a run's container. After
// the header and the ack below, the channel is an ordinary SSH transport: the
// server speaks the SSH server protocol on it, so a standard ssh client whose
// ProxyCommand is `aether ssh --stdio` talks to it unchanged.
const SubsystemRunSSH = "aether-run-ssh"

type RunSSHRequest struct {
	RunID string `json:"run_id"`
}

// RunSSHResponse acknowledges or refuses a RunSSHRequest. HostKey is the
// authorized_keys line of the key the nested SSH server presents.
type RunSSHResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Code    int    `json:"code,omitempty"`
	HostKey string `json:"host_key,omitempty"`
}
