package localgw

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/cli/profile"
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

// envScanRequest is the client's start frame.
type envScanRequest struct {
	Harness  string `json:"harness"`
	Mode     string `json:"mode"`
	RepoPath string `json:"repo_path"`
}

// envScanFrame is every frame the server sends: type discriminates, the
// other fields are set per type.
type envScanFrame struct {
	Type string `json:"type"`
	Status string `json:"status,omitempty"`
	Line string `json:"line,omitempty"`
	Recommendation *profile.Recommendation `json:"recommendation,omitempty"`
	Detail string `json:"detail,omitempty"`
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

// handleEnvScan serves the profile scan websocket.
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
	if req.Mode != localops.ScanModeProfile {
		_ = writeFrame(ctx, conn, envScanFrame{
			Type:   envScanFrameError,
			Detail: "unsupported scan mode",
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "unsupported scan mode")
		return
	}
	widenCtx, widenDone := context.WithTimeout(ctx, loginPathTimeout)
	_, _ = localops.AdoptLoginPath(widenCtx)
	widenDone()
	g.runProfileScan(ctx, conn, req)
}
