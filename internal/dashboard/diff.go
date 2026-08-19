package dashboard

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// maxPatchBytes bounds one rendered diff. The dashboard reads diffs, it
// does not download repositories: past this the view is unreadable anyway
// and the answer says it was cut short.
const maxPatchBytes = 512 << 10

// Patcher renders a run's working diff as unified patch text; satisfied by
// gitengine.Engine. The diff timeline is the one dashboard surface with no
// control-channel method behind it - run.diff events carry per-file stats
// only, so the patch text is fetched here and the events say when to
// refetch.
type Patcher interface {
	RunPatch(ctx context.Context, run domain.RunID, maxBytes int) (gitengine.Patch, error)
}

// patchResponse is the body of GET /api/v1/run/{run}/patch.
type patchResponse struct {
	RunID     string `json:"run_id"`
	Base      string `json:"base"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
}

// handlePatch serves the run's diff against the fork point its checkout
// records. Visibility is not decided here: run.get is the control-channel
// read that already answers "may this member see this run", so it runs
// first and its refusal is the response.
func (g *Gateway) handlePatch(w http.ResponseWriter, r *http.Request) {
	member, _, ok := g.authenticate(w, r, false)
	if !ok {
		return
	}
	if g.cfg.Git == nil {
		writeError(w, http.StatusServiceUnavailable, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "run.patch: diff rendering is not enabled",
		})
		return
	}
	run := r.PathValue("run")
	params, err := json.Marshal(protocol.RunIDParams{RunID: run})
	if err != nil {
		writeError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()})
		return
	}
	if resp := g.cfg.RPC.Call(r.Context(), member, protocol.MethodRunGet, params); resp.Error != nil {
		writeError(w, statusFor(resp.Error.Code), resp.Error)
		return
	}
	patch, err := g.cfg.Git.RunPatch(r.Context(), domain.RunID(run), maxPatchBytes)
	if err != nil {
		// The failure is always the same one in practice - the run's
		// checkout is gone, because the run finished and was cleaned up -
		// and the wrapped error names server paths, so it is not echoed.
		writeError(w, http.StatusServiceUnavailable, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "run.patch: this run has no checkout to diff",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(patchResponse{
		RunID:     run,
		Base:      patch.Base,
		Patch:     patch.Text,
		Truncated: patch.Truncated,
	})
}
