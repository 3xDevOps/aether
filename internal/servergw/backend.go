package servergw

import (
	"context"
	"encoding/json"
	"io"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// backend is one identified member's in-process view of the server.
type backend struct {
	local *sshd.Local
}

func (b backend) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	return b.local.Call(ctx, method, params)
}

func (b backend) Events(ctx context.Context, req protocol.SubscribeRequest) (io.ReadCloser, error) {
	return b.local.Events(ctx, req)
}

func (b backend) Attach(ctx context.Context, req protocol.AttachRequest) (webgate.Terminal, protocol.AttachResponse, error) {
	term, ack, err := b.local.Attach(ctx, req)
	if term == nil {
		return nil, ack, err
	}
	return term, ack, err
}

func (b backend) Terminal(ctx context.Context, req protocol.TerminalRequest) (webgate.Terminal, protocol.TerminalResponse, error) {
	term, ack, err := b.local.Terminal(ctx, req)
	if term == nil {
		return nil, ack, err
	}
	return term, ack, err
}
