package sshd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const developmentStreamWriteTimeout = 10 * time.Second

// developmentStreamLifetime bounds idle viewers too: authorization withdrawal
// closes the channel even when the browser has no new frame to publish.
func developmentStreamLifetime(ctx context.Context, ch subsystemConn, authorize func(context.Context) error) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = ch.Close()
				return
			case <-ticker.C:
				if err := authorize(ctx); err != nil {
					ch.exit(attachExitForError(err))
					cancel()
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}

func developmentStreamWrite(ch subsystemConn, write func() error) error {
	timer := time.AfterFunc(developmentStreamWriteTimeout, func() { _ = ch.Close() })
	defer timer.Stop()
	return write()
}

func readDevelopmentHeader(ch subsystemConn, target any) error {
	timer := time.AfterFunc(10*time.Second, func() { _ = ch.Close() })
	defer timer.Stop()
	r := bufio.NewReaderSize(&capReader{r: ch, left: protocol.MaxDevParamsBytes + 1}, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return invalidParams("invalid development stream header: " + err.Error())
	}
	if perr := decodeDevelopment(line, target); perr != nil {
		return perr
	}
	return nil
}

func refuseDevelopmentStream(ch subsystemConn, err error) {
	perr := rpcError(err)
	_ = developmentStreamWrite(ch, func() error {
		return writeJSONLine(ch, protocol.DevStreamResponse{Code: perr.Code, Error: perr.Message})
	})
}

func (s *Server) serveDevelopmentBrowser(ctx context.Context, member domain.MemberID, ch subsystemConn) {
	defer func() { _ = ch.Close() }()
	stopContext := context.AfterFunc(ctx, func() { _ = ch.Close() })
	defer stopContext()
	var req protocol.DevBrowserStreamRequest
	if err := readDevelopmentHeader(ch, &req); err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	run, principal, authorize, err := s.developmentAuthority(ctx, member, req.RunID)
	if err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	ctx, stop := developmentStreamLifetime(ctx, ch, authorize)
	defer stop()
	frames, closeFrames, err := s.cfg.Services.Development.BrowserFrames(ctx, *run, principal, req.DevBrowserPageTarget, authorize)
	if err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	defer closeFrames()
	if err := developmentStreamWrite(ch, func() error { return writeJSONLine(ch, protocol.DevStreamResponse{OK: true}) }); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				ch.exit(0)
				return
			}
			if frame.Err != nil {
				ch.exit(attachExitForError(frame.Err))
				return
			}
			if frame.Metadata.RunID != req.RunID || frame.Metadata.SessionID != req.SessionID || frame.Metadata.PageID != req.PageID {
				ch.exit(1)
				return
			}
			if err := authorize(ctx); err != nil {
				ch.exit(attachExitForError(err))
				return
			}
			if err := developmentStreamWrite(ch, func() error { return protocol.WriteDevBrowserFrame(ch, frame) }); err != nil {
				return
			}
		}
	}
}

func (s *Server) serveDevelopmentArtifact(ctx context.Context, member domain.MemberID, ch subsystemConn) {
	defer func() { _ = ch.Close() }()
	stopContext := context.AfterFunc(ctx, func() { _ = ch.Close() })
	defer stopContext()
	var req protocol.DevArtifactDownloadRequest
	if err := readDevelopmentHeader(ch, &req); err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	run, principal, authorize, err := s.developmentAuthority(ctx, member, req.RunID)
	if err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	ctx, stop := developmentStreamLifetime(ctx, ch, authorize)
	defer stop()
	artifact, source, err := s.cfg.Services.Development.OpenArtifact(ctx, *run, principal, req.ArtifactID, authorize)
	if err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	defer func() { _ = source.Close() }()
	if artifact.RunID != req.RunID || artifact.ID != req.ArtifactID || artifact.Bytes < 0 {
		refuseDevelopmentStream(ch, &protocol.Error{Code: protocol.CodeDenied, Message: "capture identity mismatch"})
		return
	}
	if err := developmentStreamWrite(ch, func() error { return writeJSONLine(ch, protocol.DevStreamResponse{OK: true, Artifact: &artifact}) }); err != nil {
		return
	}
	buf := make([]byte, 32<<10)
	remaining := artifact.Bytes
	for remaining > 0 {
		if err := authorize(ctx); err != nil {
			ch.exit(attachExitForError(err))
			return
		}
		limit := int64(len(buf))
		if remaining < limit {
			limit = remaining
		}
		n, err := io.ReadFull(source, buf[:limit])
		if err != nil && !errors.Is(err, io.EOF) {
			ch.exit(1)
			return
		}
		if n == 0 {
			ch.exit(1)
			return
		}
		if err := developmentStreamWrite(ch, func() error { _, err := ch.Write(buf[:n]); return err }); err != nil {
			return
		}
		remaining -= int64(n)
	}
	ch.exit(0)
}
