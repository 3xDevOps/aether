package cli

import (
	"errors"
	"io"

	"golang.org/x/crypto/ssh"
)

// Forward opens a direct-tcpip channel to a run container port.
func (c *Conn) Forward(runID string, port uint32) (io.ReadWriteCloser, error) {
	payload := struct {
		DestHost string
		DestPort uint32
		OrigHost string
		OrigPort uint32
	}{
		DestHost: "run:" + runID,
		DestPort: port,
		OrigHost: "127.0.0.1",
	}
	ch, reqs, err := c.client.OpenChannel("direct-tcpip", ssh.Marshal(payload))
	if err != nil {
		var openErr *ssh.OpenChannelError
		if errors.As(err, &openErr) && openErr.Reason == ssh.UnknownChannelType {
			return nil, errors.New("the server does not support port forwarding; update aether-server")
		}
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return ch, nil
}
