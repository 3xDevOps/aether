package localgw

import (
	"context"
	"errors"
	"fmt"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/localops"
)

// envScanNothingToImport is the answer for a machine with no agent
// configuration at all: a normal outcome, not a failure of the scan, so
// it names the fact instead of an internal error.
const envScanNothingToImport = "no agent configuration found on this machine; nothing to import"

// runProfileScan is the profile branch of /ws/envscan: it previews every
// harness configuration present on this machine, hands those inventories
// to the chosen agent, and ends with one result frame carrying the import
// recommendation or one error frame. The caller holds the scan slot and
// has already sent the detecting status frame.
func (g *Gateway) runProfileScan(ctx context.Context, conn *websocket.Conn, req envScanRequest) {
	previews, err := profileScanInventories()
	if err != nil {
		_ = writeFrame(ctx, conn, envScanFrame{Type: envScanFrameError, Detail: err.Error()})
		_ = conn.Close(websocket.StatusNormalClosure, "scan failed")
		return
	}
	if len(previews) == 0 {
		_ = writeFrame(ctx, conn, envScanFrame{Type: envScanFrameError, Detail: envScanNothingToImport})
		_ = conn.Close(websocket.StatusNormalClosure, "nothing to import")
		return
	}

	result, err := localops.RunProfileScan(ctx, localops.ProfileScanOptions{
		Harness:   req.Harness,
		RepoPath:  req.RepoPath,
		Inventory: previews,
		Argv:      g.local.scanArgvOverride(),
	}, func(e localops.ScanEvent) {
		// RunProfileScan calls back serially and the handler writes nothing
		// else while it runs, so frames never interleave. A write failure
		// means the client is gone; the handler's read pump is already
		// canceling.
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
	if writeFrame(ctx, conn, envScanFrame{
		Type:           envScanFrameResult,
		Recommendation: &result.Recommendation,
	}) != nil {
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "scan complete")
}

// profileScanInventories previews every harness that syncs a profile and
// has its configuration directory on this machine. A harness the user
// does not use is simply absent from the result, never an error.
func profileScanInventories() ([]profile.Preview, error) {
	var out []profile.Preview
	for _, p := range harness.Profiles() {
		if p.LocalRoot == "" {
			continue
		}
		preview, err := profile.Inventory(p.Name)
		if err != nil {
			return nil, fmt.Errorf("read the %s configuration: %w", p.Name, err)
		}
		if preview.Present {
			out = append(out, preview)
		}
	}
	return out, nil
}
