package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const maxDevelopmentCaptures = 64
const maxDevelopmentCaptureBytes int64 = 128 << 20

// Captures are the only browser-derived data under the existing read-only run
// mount. Browser journals, profiles and private sockets never live here.
func (s *Scheduler) captureDir(id domain.RunID) (string, error) {
	s.mu.Lock()
	entry := s.runs[id]
	dir := ""
	if entry != nil {
		dir = entry.coordDir
	}
	s.mu.Unlock()
	if dir == "" {
		sc, err := s.readSidecar(id)
		if err != nil {
			return "", err
		}
		dir = sc.CoordDir
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("run has no read-only capture mount; relaunch with the current server")
	}
	return filepath.Join(dir, "captures"), nil
}
func validCaptureID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func readCapture(dir string, id domain.RunID, handle string) (protocol.DevArtifact, error) {
	var artifact protocol.DevArtifact
	if !validCaptureID(handle) {
		return artifact, errors.New("invalid capture handle")
	}
	f, err := os.Open(filepath.Join(dir, handle+".json"))
	if err != nil {
		return artifact, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil {
		return artifact, err
	}
	if len(data) >= 16<<10 {
		return artifact, errors.New("capture metadata exceeds limit")
	}
	if err = json.Unmarshal(data, &artifact); err != nil {
		return artifact, err
	}
	if artifact.ID != handle || artifact.RunID != string(id) || artifact.Path != "/run/aether/captures/"+handle+".png" || artifact.Bytes <= 0 || artifact.Bytes > browser.MaxImageBytes {
		return artifact, errors.New("capture metadata identity mismatch")
	}
	info, err := os.Lstat(filepath.Join(dir, handle+".png"))
	if err != nil {
		return artifact, err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.Bytes {
		return artifact, errors.New("capture image is missing or changed")
	}
	return artifact, nil
}
func listCaptures(dir string, id domain.RunID) ([]protocol.DevArtifact, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []protocol.DevArtifact{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []protocol.DevArtifact{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		artifact, err := readCapture(dir, id, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CapturedAt == out[j].CapturedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CapturedAt < out[j].CapturedAt
	})
	return out, nil
}
func (s *Scheduler) saveDevelopmentCapture(ctx context.Context, id domain.RunID, source, terminal string, capture browser.Capture, geometry uint64) (protocol.DevArtifact, error) {
	var out protocol.DevArtifact
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if len(capture.Bytes) == 0 || len(capture.Bytes) > browser.MaxImageBytes || capture.Metadata.ContentType != "image/png" {
		return out, errors.New("invalid capture image")
	}
	d := s.developmentState()
	d.captures.Lock()
	defer d.captures.Unlock()
	dir, captureDirErr := s.captureDir(id)
	if captureDirErr != nil {
		return out, captureDirErr
	}
	if captureDirErr = os.MkdirAll(dir, 0o755); captureDirErr != nil {
		return out, captureDirErr
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	total := int64(len(capture.Bytes))
	count := 0
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".png") {
			continue
		}
		info, infoErr := file.Info()
		if infoErr != nil {
			return out, infoErr
		}
		if !info.Mode().IsRegular() {
			return out, errors.New("capture store contains a non-regular image")
		}
		total += info.Size()
		count++
	}
	// Count even an interrupted publication's image: a crash cannot evade quota.
	if count >= maxDevelopmentCaptures || total > maxDevelopmentCaptureBytes {
		return out, errors.New("run capture limit reached; delete captures before taking another")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return out, err
	}
	handle := hex.EncodeToString(nonce[:])
	m := capture.Metadata
	out = protocol.DevArtifact{ID: handle, Path: "/run/aether/captures/" + handle + ".png", Source: source, RunID: string(id), Incarnation: m.SessionID, TerminalID: terminal, PageID: m.PageID, PageRevision: m.PageRevision, ScreenRevision: m.ScreenRevision, GeometryRevision: geometry, ViewportID: m.ViewportID, URL: m.URL, CapturedAt: m.CapturedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"), ContentType: m.ContentType, Bytes: int64(len(capture.Bytes)), Width: m.Width, Height: m.Height, Cols: uint(m.Cols), Rows: uint(m.Rows)}
	// No Git boundary is claimed: rendering pixels does not atomically observe
	// checkout HEAD/dirty state, and later edits cannot be inferred from it.
	imagePath := filepath.Join(dir, handle+".png")
	f, err := os.OpenFile(imagePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
	if err != nil {
		return out, err
	}
	_, writeErr := f.Write(capture.Bytes)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(imagePath)
		return out, err
	}
	metadata, err := json.Marshal(out)
	if err != nil {
		_ = os.Remove(imagePath)
		return out, err
	}
	if len(metadata) >= 16<<10 {
		_ = os.Remove(imagePath)
		return out, errors.New("capture metadata exceeds limit")
	}
	metaPath := filepath.Join(dir, handle+".json")
	meta, err := os.OpenFile(metaPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
	if err != nil {
		_ = os.Remove(imagePath)
		return out, err
	}
	_, writeErr = meta.Write(metadata)
	syncErr = meta.Sync()
	closeErr = meta.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(imagePath)
		_ = os.Remove(metaPath)
		return out, err
	}
	if err = fsyncDir(dir); err != nil {
		return out, err
	}
	return out, nil
}
func (s *Scheduler) developmentArtifact(id domain.RunID, method string, raw json.RawMessage, auth func() error) (any, error) {
	d := s.developmentState()
	d.captures.Lock()
	defer d.captures.Unlock()
	dir, err := s.captureDir(id)
	if err != nil {
		return nil, err
	}
	switch method {
	case protocol.MethodDevArtifactList:
		var req protocol.DevArtifactListParams
		if err := decodeDevelopment(raw, &req); err != nil {
			return nil, err
		}
		if req.Limit == 0 {
			req.Limit = protocol.MaxDevArtifactPage
		}
		if req.Limit < 1 || req.Limit > protocol.MaxDevArtifactPage {
			return nil, errors.New("invalid capture page size")
		}
		captures, err := listCaptures(dir, id)
		if err != nil {
			return nil, err
		}
		start := 0
		if req.After != "" {
			found := false
			for i, capture := range captures {
				if capture.ID == req.After {
					start = i + 1
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("capture cursor no longer exists")
			}
		}
		end := start
		budget := 256
		for end < len(captures) && end-start < req.Limit {
			encoded, err := json.Marshal(captures[end])
			if err != nil {
				return nil, err
			}
			if budget+len(encoded)+1 > protocol.MaxDevResultBytes {
				break
			}
			budget += len(encoded) + 1
			end++
		}
		if end == start && start < len(captures) {
			return nil, errors.New("capture metadata exceeds response budget")
		}
		out := protocol.DevArtifactListResult{Artifacts: captures[start:end], Truncated: end < len(captures)}
		if out.Truncated {
			out.Next = captures[end-1].ID
		}
		return out, nil
	case protocol.MethodDevArtifactGet:
		var req protocol.DevArtifactGetParams
		if err := decodeDevelopment(raw, &req); err != nil {
			return nil, err
		}
		artifact, err := readCapture(dir, id, req.ArtifactID)
		return protocol.DevArtifactGetResult{Artifact: artifact}, err
	case protocol.MethodDevArtifactDelete:
		var req protocol.DevArtifactDeleteParams
		if err := decodeDevelopment(raw, &req); err != nil {
			return nil, err
		}
		if _, err := readCapture(dir, id, req.ArtifactID); err != nil {
			return nil, err
		}
		err := s.cfg.Control.Admit(string(id), func() error {
			if err := auth(); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(dir, req.ArtifactID+".png")); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(dir, req.ArtifactID+".json")); err != nil {
				return err
			}
			return fsyncDir(dir)
		})
		return protocol.DevArtifactDeleteResult{Deleted: err == nil}, err
	}
	return nil, errors.New("unknown artifact operation")
}
func (s *Scheduler) captureTerminal(ctx context.Context, id domain.RunID, raw json.RawMessage, auth func() error) (any, error) {
	var req protocol.DevTerminalScreenshotParams
	if err := decodeDevelopment(raw, &req); err != nil {
		return nil, err
	}
	live, liveErr := s.ResolveLiveRun(ctx, id, false)
	if liveErr != nil {
		return nil, liveErr
	}
	terminal, screen, terminalErr := s.CaptureDevelopmentTerminal(ctx, id, req.DevTerminalTarget)
	if terminalErr != nil {
		return nil, terminalErr
	}
	capturedAt := time.Now().UTC()
	if screen.ProtocolError != "" {
		return nil, errors.New(screen.ProtocolError)
	}
	if len(screen.UnsupportedGraphics) > 0 {
		return nil, fmt.Errorf("terminal screenshot does not support graphics: %s", strings.Join(screen.UnsupportedGraphics, ", "))
	}
	d := s.developmentState()
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	var client *browser.Client
	admitErr := s.cfg.Control.Admit(string(id), func() error {
		if err := auth(); err != nil {
			return err
		}
		current, err := s.ResolveLiveRun(ctx, id, false)
		if err != nil {
			return err
		}
		if current.ContainerID != live.ContainerID {
			return control.ErrStale
		}
		_, client, err = s.browserClient(ctx, live, true)
		return err
	})
	if admitErr != nil {
		return nil, admitErr
	}
	capture, renderErr := client.RenderTerminal(ctx, browser.TerminalSnapshot{SessionID: terminal.Incarnation, ScreenRevision: screen.Revision, OutputPosition: uint64(screen.Position.Sequence), CapturedAt: capturedAt, Cols: int(screen.Cols), Rows: int(screen.Rows), VT: string(screen.Snapshot.Data)})
	if renderErr != nil {
		return nil, renderErr
	}
	if err := auth(); err != nil {
		return nil, err
	}
	artifact, err := s.saveDevelopmentCapture(ctx, id, "terminal", terminal.TerminalID, capture, screen.GeometryRevision)
	return protocol.DevTerminalScreenshotResult{Artifact: artifact}, err
}

type admittedArtifactReader struct {
	io.ReadCloser
	authorize func() error
}

func (r *admittedArtifactReader) Read(p []byte) (int, error) {
	if err := r.authorize(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}
func (s *Scheduler) OpenDevelopmentArtifact(ctx context.Context, id domain.RunID, p control.Principal, handle string, authorize func() error) (protocol.DevArtifact, io.ReadCloser, error) {
	var artifact protocol.DevArtifact
	auth := func() error { return s.developmentAuthorization(ctx, id, p, authorize) }
	if err := auth(); err != nil {
		return artifact, nil, err
	}
	artifact, reader, err := s.OpenEvidenceArtifact(ctx, id, handle)
	if err != nil {
		return artifact, nil, err
	}
	return artifact, &admittedArtifactReader{ReadCloser: reader, authorize: auth}, nil
}
