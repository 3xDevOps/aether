package sshd

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func roomImageRequest(run domain.RunID, message string, index int) protocol.DevArtifactDownloadRequest {
	return protocol.DevArtifactDownloadRequest{
		DevArtifactGetParams: protocol.DevArtifactGetParams{DevRunParams: protocol.DevRunParams{RunID: string(run)}, ArtifactID: "room:" + message + ":" + strconv.Itoa(index)},
		RoomMessageID:        message, AttachmentIndex: &index,
	}
}

func roomImageFixture(t *testing.T, e *testEnv) *store.RoomMessage {
	t.Helper()
	message := handlerRoomMessage("image-message", e.ws.ID, e.run.ID, e.member.ID)
	message.Attachments = []string{"/home/aether/.aether/terminal-images/image-0123456789abcdef0123456789abcdef.png"}
	if err := e.store.CreateRoomMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestRoomImageViewAfterRunShutdownLocalAndSSH(t *testing.T) {
	for _, transport := range []string{"local", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			e := newTestEnv(t, nil)
			signer, viewer := addMember(t, e, "image viewer", domain.RoleViewer, false)
			e.run.Status = domain.RunCompleted
			e.run.Protected = true
			if err := e.store.UpdateRun(t.Context(), e.run); err != nil {
				t.Fatal(err)
			}
			message := roomImageFixture(t, e)
			e.srv.cfg.Services.Development = nil
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			data := encoded.Bytes()
			e.runs.readImage = func(_ context.Context, run domain.RunID, reference string) ([]byte, string, error) {
				if run != e.run.ID || reference != message.Attachments[0] {
					return nil, "", errors.New("read did not use persisted attachment")
				}
				return data, "image/png", nil
			}
			req := roomImageRequest(e.run.ID, message.ID, 0)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var source io.Reader
			if transport == "local" {
				stream, artifact, err := e.srv.Local(viewer.ID).Artifact(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stream.Close() }()
				if artifact.ID != req.ArtifactID || artifact.RunID != req.RunID || artifact.Bytes != int64(len(data)) || artifact.ContentType != "image/png" {
					t.Fatalf("room image metadata = %+v", artifact)
				}
				source = stream
			} else {
				client, _ := developmentSSHClient(t, e, signer)
				stream := openDevelopmentSSHStream(t, client, protocol.SubsystemDevArtifact, req)
				source = stream.r
			}
			got, err := io.ReadAll(source)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("room image bytes = %q, %v", got, err)
			}
		})
	}
}

func TestRoomImageRejectsForeignMissingAndMixedSelectors(t *testing.T) {
	e := newTestEnv(t, nil)
	message := roomImageFixture(t, e)
	otherRun := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "another", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunCompleted, Branch: "aether/another"}
	if err := e.store.CreateRun(t.Context(), otherRun); err != nil {
		t.Fatal(err)
	}
	foreign := handlerRoomMessage("foreign-image", e.ws.ID, otherRun.ID, e.member.ID)
	foreign.Attachments = message.Attachments
	if err := e.store.CreateRoomMessage(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	otherWorkspace := &domain.Workspace{Name: "other-images", BaseBranch: "main", Environment: domain.WorkspaceEnvironment{}}
	if err := e.store.CreateWorkspace(t.Context(), otherWorkspace); err != nil {
		t.Fatal(err)
	}
	mismatched := handlerRoomMessage("mismatched-workspace", otherWorkspace.ID, e.run.ID, e.member.ID)
	mismatched.Attachments = message.Attachments
	if err := e.store.CreateRoomMessage(t.Context(), mismatched); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	e.runs.readImage = func(context.Context, domain.RunID, string) ([]byte, string, error) {
		reads.Add(1)
		return []byte("image"), "image/png", nil
	}
	for _, tc := range []struct {
		name   string
		change func(*protocol.DevArtifactDownloadRequest)
		code   int
	}{
		{"foreign run", func(r *protocol.DevArtifactDownloadRequest) { r.RunID = string(otherRun.ID) }, protocol.CodeNotFound},
		{"foreign message", func(r *protocol.DevArtifactDownloadRequest) { *r = roomImageRequest(e.run.ID, foreign.ID, 0) }, protocol.CodeNotFound},
		{"mismatched workspace", func(r *protocol.DevArtifactDownloadRequest) { *r = roomImageRequest(e.run.ID, mismatched.ID, 0) }, protocol.CodeNotFound},
		{"missing message", func(r *protocol.DevArtifactDownloadRequest) { *r = roomImageRequest(e.run.ID, "missing", 0) }, protocol.CodeNotFound},
		{"missing run", func(r *protocol.DevArtifactDownloadRequest) { r.RunID = "missing" }, protocol.CodeNotFound},
		{"foreign index", func(r *protocol.DevArtifactDownloadRequest) { *r = roomImageRequest(e.run.ID, message.ID, 1) }, protocol.CodeNotFound},
		{"negative index", func(r *protocol.DevArtifactDownloadRequest) { *r = roomImageRequest(e.run.ID, message.ID, -1) }, protocol.CodeInvalidParams},
		{"missing index", func(r *protocol.DevArtifactDownloadRequest) { r.AttachmentIndex = nil }, protocol.CodeInvalidParams},
		{"missing room selector", func(r *protocol.DevArtifactDownloadRequest) { r.RoomMessageID = "" }, protocol.CodeInvalidParams},
		{"missing both selectors", func(r *protocol.DevArtifactDownloadRequest) { r.RoomMessageID = ""; r.AttachmentIndex = nil }, protocol.CodeInvalidParams},
		{"mixed evidence", func(r *protocol.DevArtifactDownloadRequest) { r.EvidencePacketID = "packet" }, protocol.CodeInvalidParams},
		{"noncanonical identity", func(r *protocol.DevArtifactDownloadRequest) { r.ArtifactID = "room:" + message.ID + ":00" }, protocol.CodeInvalidParams},
		{"caller path", func(r *protocol.DevArtifactDownloadRequest) { r.ArtifactID = "/etc/passwd" }, protocol.CodeInvalidParams},
		{"caller URL", func(r *protocol.DevArtifactDownloadRequest) { r.ArtifactID = "https://example.com/image.png" }, protocol.CodeInvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := roomImageRequest(e.run.ID, message.ID, 0)
			tc.change(&req)
			source, _, err := e.srv.Local(e.member.ID).Artifact(t.Context(), req)
			if source != nil {
				_ = source.Close()
			}
			var perr *protocol.Error
			if !errors.As(err, &perr) || perr.Code != tc.code {
				t.Fatalf("selector error = %v, want %d", err, tc.code)
			}
		})
	}
	if reads.Load() != 0 {
		t.Fatalf("denied requests read image bytes %d times", reads.Load())
	}
}

func TestRoomImageRevocationAndDeletionStopOpenStream(t *testing.T) {
	for _, cause := range []string{"membership", "run deletion"} {
		t.Run(cause, func(t *testing.T) {
			e := newTestEnv(t, nil)
			_, viewer := addMember(t, e, "revocable viewer", domain.RoleViewer, false)
			message := roomImageFixture(t, e)
			data := bytes.Repeat([]byte{1}, 256<<10)
			e.runs.readImage = func(context.Context, domain.RunID, string) ([]byte, string, error) { return data, "image/png", nil }
			req := roomImageRequest(e.run.ID, message.ID, 0)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			stream, _, err := e.srv.Local(viewer.ID).Artifact(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			if cause == "membership" {
				if deleteErr := e.store.DeleteMember(t.Context(), viewer.ID); deleteErr != nil {
					t.Fatal(deleteErr)
				}
			} else {
				if deleteErr := e.store.DeleteRun(t.Context(), e.run.ID); deleteErr != nil {
					t.Fatal(deleteErr)
				}
				if _, lookupErr := e.store.GetRoomMessage(t.Context(), message.ID); !errors.Is(lookupErr, store.ErrNotFound) {
					t.Fatalf("deleted run retained room message: %v", lookupErr)
				}
			}
			got, err := io.ReadAll(stream)
			var exit *protocol.RemoteExitError
			if !errors.As(err, &exit) || exit.Status == 0 || len(got) >= len(data) {
				t.Fatalf("revoked stream returned %d/%d bytes, %v", len(got), len(data), err)
			}
			if ctx.Err() != nil {
				t.Fatal("stream survived until caller deadline")
			}
			if next, _, err := e.srv.Local(viewer.ID).Artifact(t.Context(), req); err == nil {
				_ = next.Close()
				t.Fatal("revoked image reopened")
			}
		})
	}
}

func TestRoomImageBoundsMetadataAndPropagatesMissingBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mime string
		err  error
	}{
		{"empty", nil, "image/png", nil},
		{"oversized", make([]byte, domain.MaxImageBytes+1), "image/png", nil},
		{"unsafe MIME", []byte("html"), "text/html", nil},
		{"missing bytes", nil, "", store.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			message := roomImageFixture(t, e)
			e.runs.readImage = func(context.Context, domain.RunID, string) ([]byte, string, error) { return tc.data, tc.mime, tc.err }
			if source, _, err := e.srv.Local(e.member.ID).Artifact(t.Context(), roomImageRequest(e.run.ID, message.ID, 0)); err == nil {
				_ = source.Close()
				t.Fatal("invalid or missing image was served")
			}
		})
	}
}
