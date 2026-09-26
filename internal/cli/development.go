package cli

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type developmentStream struct {
	io.ReadCloser
	stop func() bool
}

func (s *developmentStream) Close() error {
	s.stop()
	return s.ReadCloser.Close()
}

func (c *Conn) openDevelopment(ctx context.Context, subsystem string, request any) (io.ReadCloser, protocol.DevStreamResponse, error) {
	var ack protocol.DevStreamResponse
	if err := ctx.Err(); err != nil {
		return nil, ack, err
	}
	stream, err := c.openSubsystem(subsystem, nil)
	if err != nil {
		return nil, ack, err
	}
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	timer := time.AfterFunc(15*time.Second, func() { _ = stream.Close() })
	defer timer.Stop()
	cleanup := func() {
		stop()
		_ = stream.Close()
	}
	line, err := json.Marshal(request)
	if err != nil {
		cleanup()
		return nil, ack, err
	}
	if len(line) > protocol.MaxDevParamsBytes {
		cleanup()
		return nil, ack, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "development header exceeds limit"}
	}
	if _, err = stream.Write(append(line, '\n')); err != nil {
		cleanup()
		return nil, ack, err
	}
	out, err := readAck(stream, &ack)
	if err != nil {
		cleanup()
		return nil, ack, err
	}
	if !ack.OK {
		cleanup()
		return nil, ack, &protocol.Error{Code: ack.Code, Message: ack.Error}
	}
	return &developmentStream{ReadCloser: out, stop: stop}, ack, nil
}

func (c *Conn) BrowserFrames(ctx context.Context, req protocol.DevBrowserStreamRequest) (io.ReadCloser, error) {
	stream, _, err := c.openDevelopment(ctx, protocol.SubsystemDevBrowser, req)
	return stream, err
}

func (c *Conn) Artifact(ctx context.Context, req protocol.DevArtifactDownloadRequest) (io.ReadCloser, protocol.DevArtifact, error) {
	stream, ack, err := c.openDevelopment(ctx, protocol.SubsystemDevArtifact, req)
	if err != nil {
		return nil, protocol.DevArtifact{}, err
	}
	if ack.Artifact == nil {
		_ = stream.Close()
		return nil, protocol.DevArtifact{}, &protocol.Error{Code: protocol.CodeInternal, Message: "capture response lacks metadata"}
	}
	return stream, *ack.Artifact, nil
}
