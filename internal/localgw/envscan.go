package localgw

import (
	"context"
	"errors"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/localops"
)

// Envscan frame types. Every frame the socket sends is JSON with a type
// field; output and status frames stream while the scan runs, and exactly
// one result or error frame ends it.
const (
	envScanFrameOutput = "output"
	envScanFrameStatus = "status"
	envScanFrameResult = "result"
	envScanFrameError  = "error"
)

// envScanStatusDetecting is the status the gateway reports between
// accepting the start frame and the engine's own statuses (running,
// validating, retrying).
const envScanStatusDetecting = "detecting"

// envScanRequest is the client's start frame: which harness to run and,
// for a refine run, the previous pair and the user's feedback.
type envScanRequest struct {
	Harness              string `json:"harness"`
	Mode                 string `json:"mode"`
	PreviousDockerfile   string `json:"previous_dockerfile"`
	PreviousManifestJSON string `json:"previous_manifest_json"`
	Feedback             string `json:"feedback"`
}

// envScanFrame is every frame the server sends: type discriminates, the
// other fields are set per type.
type envScanFrame struct {
	Type string `json:"type"`
	// Status frames: detecting, running, validating, retrying.
	Status string `json:"status,omitempty"`
	// Output frames: one raw line of agent output.
	Line string `json:"line,omitempty"`
	// Result frame: the validated pair.
	Dockerfile   string                `json:"dockerfile,omitempty"`
	ManifestJSON string                `json:"manifest_json,omitempty"`
	Manifest     []domain.ManifestItem `json:"manifest,omitempty"`
	// Error frame: what went wrong and the last agent output for diagnosis.
	Detail     string `json:"detail,omitempty"`
	OutputTail string `json:"output_tail,omitempty"`
}

// beginScan claims the gateway's single scan slot; false means a scan is
// already running. One scan at a time is plenty for onboarding, and the
// limit keeps a stray reconnect from racing two agents over one machine.
func (s *localState) beginScan() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanActive {
		return false
	}
	s.scanActive = true
	return true
}

// endScan frees the scan slot.
func (s *localState) endScan() {
	s.mu.Lock()
	s.scanActive = false
	s.mu.Unlock()
}

// setScanArgv installs an argv override for every scan this gateway runs,
// substituting the harness's own headless command. Tests drive stub
// executables through it.
func (s *localState) setScanArgv(argv []string) {
	s.mu.Lock()
	s.scanArgv = argv
	s.mu.Unlock()
}

func (s *localState) scanArgvOverride() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanArgv
}

// handleEnvScan serves GET /ws/envscan: one environment inventory scan on
// this machine, streamed as JSON frames. The client sends a start frame,
// receives output and status frames while the chosen harness runs, and one
// terminal result or error frame. Closing the socket cancels the scan and
// kills its process.
func (g *Gateway) handleEnvScan(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, true) {
		g.deny(w)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var req envScanRequest
	readCtx, readDone := context.WithTimeout(ctx, readHeaderTimeout)
	err = wsjson.Read(readCtx, conn, &req)
	readDone()
	if err != nil {
		return
	}

	if !g.local.beginScan() {
		_ = writeFrame(ctx, conn, envScanFrame{
			Type:   envScanFrameError,
			Detail: "an environment scan is already running; wait for it to finish or cancel it first",
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "scan already running")
		return
	}
	defer g.local.endScan()

	// Frames after the start frame are discarded; the read loop noticing
	// the peer go away cancels the scan, which kills the agent process.
	go func() {
		defer cancel()
		for {
			if _, _, readErr := conn.Read(ctx); readErr != nil {
				return
			}
		}
	}()

	if writeFrame(ctx, conn, envScanFrame{Type: envScanFrameStatus, Status: envScanStatusDetecting}) != nil {
		return
	}
	result, err := localops.RunScan(ctx, localops.ScanOptions{
		Harness:              req.Harness,
		Mode:                 req.Mode,
		PreviousDockerfile:   req.PreviousDockerfile,
		PreviousManifestJSON: req.PreviousManifestJSON,
		Feedback:             req.Feedback,
		Argv:                 g.local.scanArgvOverride(),
	}, func(e localops.ScanEvent) {
		// RunScan calls back serially and the handler writes nothing else
		// while it runs, so frames never interleave. A write failure means
		// the client is gone; the read pump above is already canceling.
		if e.Status != "" {
			_ = writeFrame(ctx, conn, envScanFrame{Type: envScanFrameStatus, Status: e.Status})
			return
		}
		_ = writeFrame(ctx, conn, envScanFrame{Type: envScanFrameOutput, Line: e.Line})
	})
	if err != nil {
		frame := envScanFrame{Type: envScanFrameError, Detail: err.Error()}
		var failure *localops.ScanFailure
		if errors.As(err, &failure) {
			frame.OutputTail = failure.OutputTail
		}
		_ = writeFrame(ctx, conn, frame)
		_ = conn.Close(websocket.StatusNormalClosure, "scan failed")
		return
	}
	err = writeFrame(ctx, conn, envScanFrame{
		Type:         envScanFrameResult,
		Dockerfile:   result.Dockerfile,
		ManifestJSON: result.ManifestJSON,
		Manifest:     result.Manifest,
	})
	if err != nil {
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "scan complete")
}
