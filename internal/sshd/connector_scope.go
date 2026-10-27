package sshd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The connector scopes a member grants on the consent page
// (docs/connectors.md#scopes). Read is always granted.
const (
	ConnectorScopeRead    = "read"
	ConnectorScopeWrite   = "write"
	ConnectorScopeControl = "control"
)

// scopeBox is the consent page's checkbox of each scope it asks about.
var scopeBox = map[string]string{
	ConnectorScopeWrite:   "Launch and message",
	ConnectorScopeControl: "Stop and approve",
}

var (
	scopeRead    = []string{ConnectorScopeRead}
	scopeWrite   = []string{ConnectorScopeWrite}
	scopeControl = []string{ConnectorScopeControl}
)

// connectorMethodScopes is the scopes a connector call of each method needs
// one of. A connector may call no method missing here, whatever its
// member's role. run.input.respond also needs the scope of the request it
// answers (connectorAnswerRefusal), and run.review the scope of the run it
// would close (connectorReviewReportedOnly).
var connectorMethodScopes = map[string][]string{
	protocol.MethodServerInfo:               scopeRead,
	protocol.MethodWorkspaceList:            scopeRead,
	protocol.MethodWorkspaceGet:             scopeRead,
	protocol.MethodWorkspaceTimeline:        scopeRead,
	protocol.MethodAgentList:                scopeRead,
	protocol.MethodAgentModels:              scopeRead,
	protocol.MethodAccountList:              scopeRead,
	protocol.MethodAccountUsage:             scopeRead,
	protocol.MethodRunList:                  scopeRead,
	protocol.MethodRunGet:                   scopeRead,
	protocol.MethodRunACPHistory:            scopeRead,
	protocol.MethodTerminalHistory:          scopeRead,
	protocol.MethodRunGitLog:                scopeRead,
	protocol.MethodRunGitDiff:               scopeRead,
	protocol.MethodRunEvidenceList:          scopeRead,
	protocol.MethodRunOverlaps:              scopeRead,
	protocol.MethodRunPRStatus:              scopeRead,
	protocol.MethodRunPRFeedback:            scopeRead,
	protocol.MethodAttentionList:            scopeRead,
	protocol.MethodCoordMessagesList:        scopeRead,
	protocol.MethodRunRoomList:              scopeRead,
	protocol.MethodInsightsGet:              scopeRead,
	protocol.MethodMissionList:              scopeRead,
	protocol.MethodMissionShow:              scopeRead,
	protocol.MethodMemorySearch:             scopeRead,
	protocol.MethodMemoryShow:               scopeRead,
	protocol.MethodMemberList:               scopeRead,
	protocol.MethodIntegrationList:          scopeRead,
	protocol.MethodIntegrationShow:          scopeRead,
	protocol.MethodTemplateList:             scopeRead,
	protocol.MethodChangesList:              scopeRead,
	protocol.MethodRunChildren:              scopeRead,
	protocol.MethodRunLaunch:                scopeWrite,
	protocol.MethodTemplateLaunch:           scopeWrite,
	protocol.MethodRunRelaunch:              scopeWrite,
	protocol.MethodMissionCreate:            scopeWrite,
	protocol.MethodRunInject:                scopeWrite,
	protocol.MethodRunRoomPost:              scopeWrite,
	protocol.MethodRunInputRespond:          {ConnectorScopeWrite, ConnectorScopeControl},
	protocol.MethodMissionQuestionAnswer:    scopeWrite,
	protocol.MethodRunPause:                 scopeWrite,
	protocol.MethodRunResume:                scopeWrite,
	protocol.MethodRunReview:                scopeWrite,
	protocol.MethodRunProtect:               scopeWrite,
	protocol.MethodRunArchive:               scopeWrite,
	protocol.MethodMemoryAdd:                scopeWrite,
	protocol.MethodMemoryPin:                scopeWrite,
	protocol.MethodMemoryUnpin:              scopeWrite,
	protocol.MethodRunKill:                  scopeControl,
	protocol.MethodRunClose:                 scopeControl,
	protocol.MethodMissionCancel:            scopeControl,
	protocol.MethodRunPRCreate:              scopeControl,
	protocol.MethodRunPRMerge:               scopeControl,
	protocol.MethodRunModeSwitch:            scopeControl,
	protocol.MethodRunHandoff:               scopeControl,
	protocol.MethodMissionWorkerRelease:     scopeControl,
	protocol.MethodMissionArchive:           scopeControl,
	protocol.MethodMissionUnarchive:         scopeControl,
	protocol.MethodMissionReplaceIntegrator: scopeControl,
	protocol.MethodIntegrationDecide:        scopeControl,
	protocol.MethodIntegrationDeliver:       scopeControl,
}

// ConnectorMethodScopes are the scopes a connector needs one of to call
// method, and false for a method no connector may call.
func ConnectorMethodScopes(method string) ([]string, bool) {
	scopes, ok := connectorMethodScopes[method]
	return slices.Clone(scopes), ok
}

// connectorScopeRefusal refuses method to a connector call whose grant
// lacks the method's scope, and every unlisted method to any connector.
// Other callers pass.
func connectorScopeRefusal(ctx context.Context, method string) *protocol.Error {
	id, ok := ctx.Value(connIdentityKey{}).(connIdentity)
	if !ok || id.connector == "" {
		return nil
	}
	label := ConnectorLabel(id.connectorClient, id.connector)
	scopes, listed := connectorMethodScopes[method]
	if !listed {
		return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"%s: %s cannot call this method; no connector can, so use the Aether app or the aether CLI", method, label)}
	}
	return scopeRefusal(id, method, scopes)
}

// scopeRefusal refuses method to connector id unless it holds one of
// scopes.
func scopeRefusal(id connIdentity, method string, scopes []string) *protocol.Error {
	if slices.ContainsFunc(scopes, func(s string) bool { return slices.Contains(id.scopes, s) }) {
		return nil
	}
	boxes := make([]string, len(scopes))
	for i, s := range scopes {
		boxes[i] = scopeBox[s]
	}
	return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
		"%s needs the %s scope, which %s was not granted; select Reconnect next to it in Settings → ChatGPT and Claude and check %s on the consent page",
		method, strings.Join(scopes, " or "), ConnectorLabel(id.connectorClient, id.connector), strings.Join(boxes, " or "))}
}

// connectorAnswerRefusal refuses a connector's answer to request on run
// unless it holds control for a permission request or write for any
// other.
func (s *Server) connectorAnswerRefusal(ctx context.Context, run domain.RunID, request string) *protocol.Error {
	id, ok := ctx.Value(connIdentityKey{}).(connIdentity)
	if !ok || id.connector == "" {
		return nil
	}
	need := scopeWrite
	for _, r := range s.cfg.Runs.ACPPending(run) {
		if r.ID == request && r.Kind == acphost.RequestPermission {
			need = scopeControl
		}
	}
	return scopeRefusal(id, protocol.MethodRunInputRespond, need)
}

// connectorReviewReportedOnly is whether the call is a connector's without
// control, which may complete a live run only when its reported success
// awaits review: completing any other live run ends it, as run.close would.
// The scheduler checks it against the state it completes, so a relaunch
// cannot slip in between.
func connectorReviewReportedOnly(ctx context.Context) bool {
	id, ok := ctx.Value(connIdentityKey{}).(connIdentity)
	return ok && id.connector != "" && scopeRefusal(id, protocol.MethodRunReview, scopeControl) != nil
}
