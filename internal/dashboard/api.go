package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// maxRequestBody bounds one API request body. The dashboard makes control
// calls, not blob pushes: the 32 MiB the SSH control channel allows for
// profile uploads has no counterpart here.
const maxRequestBody = 1 << 20

// apiMethods is the set of control-channel methods the browser transport
// may call. It is an allowlist, not a blocklist: a bearer token travels in
// a URL and is therefore far easier to capture than an SSH key, so it must
// never reach a method that issues or widens a credential
// (member.invite, member.approve, member.remove, dash.token.*), replaces
// what is mounted into run containers (profile.*), or administers the
// deployment (workspace.add, session.new, session.settings, budget.set,
// template.save/delete, schedule.*). Those stay SSH-only, where the key is
// the credential. Everything here is what the dashboard actually renders
// and steers, and every entry still passes the same capability checks the
// SSH transport applies.
var apiMethods = map[string]struct{}{
	protocol.MethodServerInfo:    {},
	protocol.MethodWorkspaceList: {},
	protocol.MethodSessionList:   {},
	protocol.MethodSessionGet:    {},
	protocol.MethodMemberList:    {},

	protocol.MethodRunLaunch:  {},
	protocol.MethodRunList:    {},
	protocol.MethodRunGet:     {},
	protocol.MethodRunKill:    {},
	protocol.MethodRunPause:   {},
	protocol.MethodRunResume:  {},
	protocol.MethodRunInject:  {},
	protocol.MethodRunClose:   {},
	protocol.MethodRunHandoff: {},

	// The team surfaces the dashboard renders: the approval inbox, the
	// presence roster, the session timeline, cost and budget readouts, the
	// conflict radar, and templates.
	protocol.MethodApprovalList:      {},
	protocol.MethodApprovalDecide:    {},
	protocol.MethodPresenceRoster:    {},
	protocol.MethodPresenceHeartbeat: {},
	protocol.MethodSessionTimeline:   {},
	protocol.MethodCostReport:        {},
	protocol.MethodBudgetGet:         {},
	protocol.MethodRunOverlaps:       {},
	protocol.MethodTemplateList:      {},
	protocol.MethodTemplateLaunch:    {},
}

// handleAPI serves POST /api/v1/{method}: the path segment is the
// control-channel method name and the body is its params, dispatched
// through the same handler the SSH transport calls.
func (g *Gateway) handleAPI(w http.ResponseWriter, r *http.Request) {
	member, _, ok := g.authenticate(w, r, false)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		webgate.WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "read body: " + err.Error()})
		return
	}
	params := json.RawMessage(bytes.TrimSpace(body))
	if len(params) == 0 {
		params = nil
	} else if !json.Valid(params) {
		webgate.WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "request body is not valid JSON"})
		return
	}
	method := r.PathValue("method")
	if _, ok := apiMethods[method]; !ok {
		webgate.WriteError(w, http.StatusForbidden, &protocol.Error{
			Code:    protocol.CodeDenied,
			Message: method + " is available on the SSH control channel only",
		})
		return
	}
	resp := g.cfg.RPC.Call(r.Context(), member, method, params)
	if resp.Error != nil {
		webgate.WriteError(w, webgate.StatusFor(resp.Error.Code), resp.Error)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp.Result)
}

// errorBody is the failure envelope every endpoint answers with, shared
// with the local gateway through webgate.
type errorBody = webgate.ErrorBody
