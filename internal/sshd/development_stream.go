package sshd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const developmentStreamWriteTimeout = 10 * time.Second

// developmentStreamLifetime bounds idle viewers too. Finish cancels source work
// before attempting exit-status or channel-close, both of which can block on SSH.
// The first terminal result wins so cancellation cannot replace a useful denial.
func developmentStreamLifetime(ctx context.Context, ch subsystemConn, authorize func(context.Context) error) (context.Context, func(int)) {
	ctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	finish := func(status int) {
		once.Do(func() {
			cancel()
			if status >= 0 {
				ch.exit(status)
			}
			_ = ch.Close()
		})
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				finish(-1)
				return
			case <-ticker.C:
				if err := authorize(ctx); err != nil {
					finish(attachExitForError(err))
					return
				}
			}
		}
	}()
	return ctx, finish
}

func developmentStreamWrite(interrupt func(), write func() error) error {
	timer := time.AfterFunc(developmentStreamWriteTimeout, interrupt)
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
	_ = developmentStreamWrite(func() { _ = ch.Close() }, func() error {
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
	ctx, finish := developmentStreamLifetime(ctx, ch, authorize)
	defer finish(-1)
	frames, closeFrames, err := s.cfg.Services.Development.BrowserFrames(ctx, *run, principal, req.DevBrowserPageTarget, authorize)
	if err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	closeSource := sync.OnceFunc(closeFrames)
	defer closeSource()
	stopSource := context.AfterFunc(ctx, closeSource)
	defer stopSource()
	interrupt := func() { finish(1) }
	if err := developmentStreamWrite(interrupt, func() error { return writeJSONLine(ch, protocol.DevStreamResponse{OK: true}) }); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				finish(0)
				return
			}
			if frame.Err != nil {
				finish(attachExitForError(frame.Err))
				return
			}
			if frame.Metadata.RunID != req.RunID || frame.Metadata.SessionID != req.SessionID || frame.Metadata.PageID != req.PageID {
				finish(1)
				return
			}
			if err := authorize(ctx); err != nil {
				finish(attachExitForError(err))
				return
			}
			if err := developmentStreamWrite(interrupt, func() error { return protocol.WriteDevBrowserFrame(ch, frame) }); err != nil {
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
	artifact, source, authorize, err := s.openDevelopmentArtifact(ctx, member, req)
	if err != nil {
		refuseDevelopmentStream(ch, err)
		return
	}
	ctx, finish := developmentStreamLifetime(ctx, ch, authorize)
	defer finish(-1)
	closeSource := sync.OnceFunc(func() { _ = source.Close() })
	defer closeSource()
	stopSource := context.AfterFunc(ctx, closeSource)
	defer stopSource()
	interrupt := func() { finish(1) }
	if artifact.RunID != req.RunID || artifact.ID != req.ArtifactID || artifact.Bytes < 0 {
		closeSource()
		refuseDevelopmentStream(ch, &protocol.Error{Code: protocol.CodeDenied, Message: "capture identity mismatch"})
		return
	}
	if err := authorize(ctx); err != nil {
		closeSource()
		refuseDevelopmentStream(ch, err)
		return
	}
	if err := developmentStreamWrite(interrupt, func() error { return writeJSONLine(ch, protocol.DevStreamResponse{OK: true, Artifact: &artifact}) }); err != nil {
		return
	}
	buf := make([]byte, 32<<10)
	remaining := artifact.Bytes
	for remaining > 0 {
		if err := authorize(ctx); err != nil {
			finish(attachExitForError(err))
			return
		}
		limit := int64(len(buf))
		if remaining < limit {
			limit = remaining
		}
		n, err := io.ReadFull(source, buf[:limit])
		if err != nil && !errors.Is(err, io.EOF) {
			finish(1)
			return
		}
		if n == 0 {
			finish(1)
			return
		}
		if err := authorize(ctx); err != nil {
			finish(attachExitForError(err))
			return
		}
		if err := developmentStreamWrite(interrupt, func() error { _, err := ch.Write(buf[:n]); return err }); err != nil {
			return
		}
		remaining -= int64(n)
	}
	finish(0)
}

func (s *Server) openDevelopmentArtifact(ctx context.Context, member domain.MemberID, req protocol.DevArtifactDownloadRequest) (protocol.DevArtifact, io.ReadCloser, func(context.Context) error, error) {
	if req.RunID == "" || req.ArtifactID == "" {
		return protocol.DevArtifact{}, nil, nil, invalidParams("run_id and artifact_id are required")
	}
	if req.RoomMessageID != "" || req.AttachmentIndex != nil {
		if req.EvidencePacketID != "" || req.RoomMessageID == "" || req.AttachmentIndex == nil || *req.AttachmentIndex < 0 {
			return protocol.DevArtifact{}, nil, nil, invalidParams("room_message_id and a nonnegative attachment_index are required and cannot select evidence")
		}
		if req.ArtifactID != "room:"+req.RoomMessageID+":"+strconv.Itoa(*req.AttachmentIndex) {
			return protocol.DevArtifact{}, nil, nil, invalidParams("invalid room image artifact_id")
		}
		return s.openRoomImage(ctx, member, req)
	}
	if strings.HasPrefix(req.ArtifactID, "room:") {
		return protocol.DevArtifact{}, nil, nil, invalidParams("room image selectors are required")
	}
	if req.EvidencePacketID == "" {
		run, principal, authorize, err := s.developmentAuthority(ctx, member, req.RunID)
		if err != nil {
			return protocol.DevArtifact{}, nil, nil, err
		}
		artifact, source, err := s.cfg.Services.Development.OpenArtifact(ctx, *run, principal, req.ArtifactID, authorize)
		return artifact, source, authorize, err
	}
	service, ok := s.cfg.Services.Evidence.(EvidenceArtifactService)
	if !ok {
		return protocol.DevArtifact{}, nil, nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "retained capture service unavailable"}
	}
	initial, lookupErr := s.cfg.Store.GetEvidencePacket(ctx, req.EvidencePacketID)
	if lookupErr != nil {
		return protocol.DevArtifact{}, nil, nil, lookupErr
	}
	workspace := initial.WorkspaceID
	// The retained packet, not the live development session, is the authority
	// boundary. Reuse the evidence View policy and expiry on every recheck.
	authorize := func(ctx context.Context) error {
		if err := s.checkMember(ctx, member); err != nil {
			return err
		}
		packet, err := s.cfg.Store.GetEvidencePacket(ctx, req.EvidencePacketID)
		if err != nil {
			return err
		}
		if packet.RunID != domain.RunID(req.RunID) || packet.WorkspaceID != workspace {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "evidence packet not found"}
		}
		target, err := resolveRunTarget(ctx, s.cfg.Store, packet.RunID)
		if err != nil {
			return err
		}
		if target.Workspace != packet.WorkspaceID {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "evidence packet not found"}
		}
		actor, err := resolveActor(ctx, s.cfg.Store, member)
		if err != nil {
			return err
		}
		if permissionErr := permissions.Check(permissions.View, actor, target); permissionErr != nil {
			return permissionErr
		}
		if packet.Availability == store.EvidenceExpired || (packet.ExpiresAt != nil && !time.Now().Before(*packet.ExpiresAt)) {
			return collaborationRPCError(evidence.ErrExpired)
		}
		for _, capture := range packet.Captures {
			if capture.ID == req.ArtifactID && capture.RunID == req.RunID {
				return nil
			}
		}
		return &protocol.Error{Code: protocol.CodeNotFound, Message: "capture is not retained in this evidence packet"}
	}
	if err := authorize(ctx); err != nil {
		return protocol.DevArtifact{}, nil, nil, err
	}
	artifact, source, err := service.OpenArtifact(ctx, workspace, req.EvidencePacketID, req.ArtifactID)
	if err != nil {
		return protocol.DevArtifact{}, nil, nil, collaborationRPCError(err)
	}
	return artifact, source, authorize, err
}

// Room images use persisted message references and View, not the live
// development session's Steer/account-use authority.
func (s *Server) openRoomImage(ctx context.Context, member domain.MemberID, req protocol.DevArtifactDownloadRequest) (protocol.DevArtifact, io.ReadCloser, func(context.Context) error, error) {
	index := *req.AttachmentIndex
	message, lookupErr := s.cfg.Store.GetRoomMessage(ctx, req.RoomMessageID)
	if lookupErr != nil {
		return protocol.DevArtifact{}, nil, nil, lookupErr
	}
	if message.RunID != domain.RunID(req.RunID) || index >= len(message.Attachments) {
		return protocol.DevArtifact{}, nil, nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "room image not found"}
	}
	workspace, reference := message.WorkspaceID, message.Attachments[index]
	authorize := func(ctx context.Context) error {
		if err := s.checkMember(ctx, member); err != nil {
			return err
		}
		current, err := s.cfg.Store.GetRoomMessage(ctx, req.RoomMessageID)
		if err != nil {
			return err
		}
		if current.RunID != domain.RunID(req.RunID) || current.WorkspaceID != workspace || index >= len(current.Attachments) || current.Attachments[index] != reference {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "room image not found"}
		}
		target, err := resolveRunTarget(ctx, s.cfg.Store, current.RunID)
		if err != nil {
			return err
		}
		if target.Workspace != workspace {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "room image not found"}
		}
		actor, err := resolveActor(ctx, s.cfg.Store, member)
		if err != nil {
			return err
		}
		return permissions.Check(permissions.View, actor, target)
	}
	if err := authorize(ctx); err != nil {
		return protocol.DevArtifact{}, nil, nil, err
	}
	data, mimeType, err := s.cfg.Runs.ReadImage(ctx, message.RunID, reference)
	if err != nil {
		return protocol.DevArtifact{}, nil, nil, err
	}
	if len(data) == 0 || len(data) > domain.MaxImageBytes {
		return protocol.DevArtifact{}, nil, nil, invalidParams("invalid room image size")
	}
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return protocol.DevArtifact{}, nil, nil, invalidParams("invalid room image content type")
	}
	artifact := protocol.DevArtifact{ID: req.ArtifactID, RunID: req.RunID, ContentType: mimeType, Bytes: int64(len(data))}
	return artifact, io.NopCloser(bytes.NewReader(data)), authorize, nil
}
