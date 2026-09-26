package sshd

import (
	"context"
	"io"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func (l *Local) BrowserFrames(ctx context.Context, req protocol.DevBrowserStreamRequest) (io.ReadCloser, error) {
	var ack protocol.DevStreamResponse
	stream, err := l.open(ctx, req, &ack, func(ctx context.Context, ch subsystemConn) { l.s.serveDevelopmentBrowser(ctx, l.member, ch) })
	if err != nil {
		return nil, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, &protocol.Error{Code: ack.Code, Message: ack.Error}
	}
	return stream, nil
}

func (l *Local) Artifact(ctx context.Context, req protocol.DevArtifactDownloadRequest) (io.ReadCloser, protocol.DevArtifact, error) {
	var ack protocol.DevStreamResponse
	stream, err := l.open(ctx, req, &ack, func(ctx context.Context, ch subsystemConn) { l.s.serveDevelopmentArtifact(ctx, l.member, ch) })
	if err != nil {
		return nil, protocol.DevArtifact{}, err
	}
	if !ack.OK {
		_ = stream.Close()
		return nil, protocol.DevArtifact{}, &protocol.Error{Code: ack.Code, Message: ack.Error}
	}
	if ack.Artifact == nil {
		_ = stream.Close()
		return nil, protocol.DevArtifact{}, &protocol.Error{Code: protocol.CodeInternal, Message: "capture response lacks metadata"}
	}
	return stream, *ack.Artifact, nil
}
