package webgate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func developmentBackend(backend Backend) (DevelopmentBackend, *protocol.Error) {
	stream, ok := backend.(DevelopmentBackend)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "development streams unavailable"}
	}
	return stream, nil
}

func developmentError(err error) *protocol.Error {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr
	}
	var exit *protocol.RemoteExitError
	if errors.As(err, &exit) && (exit.Status == protocol.AttachExitMembershipRevoked || exit.Status == protocol.AttachExitSteerRevoked || exit.Status == protocol.AttachExitControlRevoked) {
		return &protocol.Error{Code: protocol.CodeDenied, Message: "development authority withdrawn"}
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
}

// Browser frames are one complete binary message each: an eight-byte big endian
// metadata/payload length prefix, bounded JSON metadata, then image bytes. The
// single-slot queue drops obsolete frames, never piling up images behind a slow
// WebSocket. Input uses dev.browser.action, not this observation-only stream.
func (g *Gateway) handleDevelopmentBrowser(w http.ResponseWriter, r *http.Request) {
	s, ok := g.Accept(w, r)
	if !ok {
		return
	}
	defer s.Close()
	refuse := func(perr *protocol.Error) {
		_ = s.WriteJSON(protocol.DevStreamResponse{Code: perr.Code, Error: perr.Message})
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "browser stream refused")
	}
	var req protocol.DevBrowserStreamRequest
	if err := s.ReadHeader(&req); err != nil {
		return
	}
	if req.RunID != "" && req.RunID != r.PathValue("run") {
		refuse(&protocol.Error{Code: protocol.CodeDenied, Message: "stream run does not match route"})
		return
	}
	req.RunID = r.PathValue("run")
	backend, perr := developmentBackend(s.Backend)
	if perr != nil {
		refuse(perr)
		return
	}
	source, err := backend.BrowserFrames(s.Ctx, req)
	if err != nil {
		refuse(developmentError(err))
		return
	}
	defer func() { _ = source.Close() }()
	stop := context.AfterFunc(s.Ctx, func() { _ = source.Close() })
	defer stop()
	if err := s.WriteJSON(protocol.DevStreamResponse{OK: true}); err != nil {
		return
	}
	readCtx := s.Conn.CloseRead(s.Ctx)
	stopRead := context.AfterFunc(readCtx, s.cancel)
	defer stopRead()
	frames, finished := latestDevelopmentFrames(s.Ctx, source, req)
	writeFrame := func(frame protocol.DevBrowserFrame) error {
		ctx, cancel := context.WithTimeout(s.Ctx, wsWriteTimeout)
		defer cancel()
		writer, err := s.Conn.Writer(ctx, websocket.MessageBinary)
		if err != nil {
			return err
		}
		if err := protocol.WriteDevBrowserFrame(writer, frame); err != nil {
			return err
		}
		return writer.Close()
	}
	endStream := func(err error) {
		if errors.Is(err, io.EOF) {
			select {
			case frame := <-frames:
				if writeFrame(frame) != nil {
					return
				}
			default:
			}
			_ = s.Conn.Close(websocket.StatusNormalClosure, "browser stream ended")
			return
		}
		perr := developmentError(err)
		_ = s.WriteJSON(protocol.DevStreamResponse{Code: perr.Code, Error: perr.Message})
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "browser stream ended")
	}
	for {
		select {
		case <-s.Ctx.Done():
			return
		case err := <-finished:
			endStream(err)
			return
		case frame := <-frames:
			// A revocation already read from the upstream stream takes
			// precedence over any previously queued image.
			select {
			case err := <-finished:
				if errors.Is(err, io.EOF) && writeFrame(frame) != nil {
					return
				}
				endStream(err)
				return
			default:
			}
			if writeFrame(frame) != nil {
				return
			}
		}
	}
}

func latestDevelopmentFrames(ctx context.Context, source io.Reader, req protocol.DevBrowserStreamRequest) (<-chan protocol.DevBrowserFrame, <-chan error) {
	frames := make(chan protocol.DevBrowserFrame, 1)
	finished := make(chan error, 1)
	go func() {
		for {
			frame, err := protocol.ReadDevBrowserFrame(source)
			if err != nil {
				finished <- err
				return
			}
			if frame.Metadata.RunID != req.RunID || frame.Metadata.SessionID != req.SessionID || frame.Metadata.PageID != req.PageID {
				finished <- &protocol.Error{Code: protocol.CodeDenied, Message: "browser frame identity mismatch"}
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			select {
			case frames <- frame:
			default:
				select {
				case <-frames:
				default:
				}
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return frames, finished
}

func (g *Gateway) handleDevelopmentArtifact(w http.ResponseWriter, r *http.Request) {
	backend, ok := g.Authorize(w, r, false)
	if !ok {
		return
	}
	stream, perr := developmentBackend(backend)
	if perr != nil {
		WriteError(w, StatusFor(perr.Code), perr)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stopGateway := context.AfterFunc(g.ctx, cancel)
	defer stopGateway()
	req := protocol.DevArtifactDownloadRequest{
		DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: r.PathValue("run")}, ArtifactID: r.PathValue("artifact")},
		EvidencePacketID:     r.URL.Query().Get("evidence_packet_id"),
	}
	if messageID := r.PathValue("message"); messageID != "" {
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil || index < 0 || req.EvidencePacketID != "" {
			WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid room image selector"})
			return
		}
		req.RoomMessageID = messageID
		req.AttachmentIndex = &index
		req.ArtifactID = "room:" + messageID + ":" + strconv.Itoa(index)
	}
	source, artifact, err := stream.Artifact(ctx, req)
	if err != nil {
		perr := developmentError(err)
		WriteError(w, StatusFor(perr.Code), perr)
		return
	}
	defer func() { _ = source.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = source.Close() })
	defer stop()
	if artifact.RunID != req.RunID || artifact.ID != req.ArtifactID || artifact.Bytes < 0 {
		WriteError(w, http.StatusForbidden, &protocol.Error{Code: protocol.CodeDenied, Message: "capture identity mismatch"})
		return
	}
	if req.RoomMessageID != "" {
		if artifact.Bytes == 0 || artifact.Bytes > domain.MaxImageBytes {
			WriteError(w, http.StatusForbidden, &protocol.Error{Code: protocol.CodeDenied, Message: "invalid room image size"})
			return
		}
		switch artifact.ContentType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			WriteError(w, http.StatusForbidden, &protocol.Error{Code: protocol.CodeDenied, Message: "invalid room image content type"})
			return
		}
	}
	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.Bytes, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="capture"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	controller := http.NewResponseController(w)
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	buf := make([]byte, 32<<10)
	remaining := artifact.Bytes
	for remaining > 0 {
		limit := int64(len(buf))
		if remaining < limit {
			limit = remaining
		}
		n, err := io.ReadFull(source, buf[:limit])
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		if ctx.Err() != nil {
			panic(http.ErrAbortHandler)
		}
		_ = controller.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		remaining -= int64(n)
	}
}
