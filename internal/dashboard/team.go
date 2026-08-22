package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// diskResponse is the body of GET /api/v1/disk: the headroom on the
// filesystem holding the server's data directory, which the status bar
// renders as a gauge, plus the three Aether directories that grow without
// bound and are what an operator can actually act on.
//
// It is a route of its own rather than a field on server.info because
// protocol.ServerInfoResult is a shared frozen type and this is a
// gateway-local read - the CLI has no use for it.
type diskResponse struct {
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	// FreeBytes is what an unprivileged writer can still claim, which is
	// the number the scheduler's free-space floor is checked against.
	FreeBytes uint64 `json:"free_bytes"`
	// The growing tenants: run checkouts (garbage-collected after their
	// TTL), transcripts (kept for the life of the run row), and the SQLite
	// file the persisted event log shares with the store.
	WorktreeBytes   uint64 `json:"worktree_bytes"`
	TranscriptBytes uint64 `json:"transcript_bytes"`
	DatabaseBytes   uint64 `json:"database_bytes"`
}

// handleDisk serves the data directory's disk usage. Any member holding a
// token may read it: it says how much room the deployment has left, not
// what anyone is running.
func (g *Gateway) handleDisk(w http.ResponseWriter, r *http.Request) {
	member, _, ok := g.authenticate(w, r, false)
	if !ok {
		return
	}
	if perr := g.cfg.RPC.CheckMember(r.Context(), member); perr != nil {
		webgate.WriteError(w, webgate.StatusFor(perr.Code), perr)
		return
	}
	if g.disk == nil {
		webgate.WriteError(w, http.StatusServiceUnavailable, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "the gateway was not told where the data directory is",
		})
		return
	}
	usage, err := g.disk.Usage()
	if err != nil {
		// The wrapped error names the data-directory path, so it is not
		// echoed - the same rule the patch endpoint applies.
		webgate.WriteError(w, http.StatusServiceUnavailable, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "disk usage: the data directory's filesystem could not be read",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(diskResponse{
		UsedBytes:       usage.UsedBytes,
		TotalBytes:      usage.TotalBytes,
		FreeBytes:       usage.FreeBytes,
		WorktreeBytes:   usage.WorktreeBytes,
		TranscriptBytes: usage.TranscriptBytes,
		DatabaseBytes:   usage.DatabaseBytes,
	})
}
