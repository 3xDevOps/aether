package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

type integrationInput struct {
	WorkspaceID string `json:"workspace_id"`
	SwarmID     string `json:"swarm_id,omitempty" jsonschema:"only this swarm's candidates"`
	CandidateID string `json:"candidate_id,omitempty" jsonschema:"show this candidate instead of listing"`
}

type integrationDecideInput struct {
	WorkspaceID string `json:"workspace_id"`
	CandidateID string `json:"candidate_id"`
	RequestID   string `json:"request_id" jsonschema:"delivery_request.request_id from aether_integration"`
	Action      string `json:"action"`
}

var integrationOps = []Op{
	opOf("aether_integration", "Integration candidates", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks what a swarm's integrator wants to deliver: integration candidates, their verifications and pending delivery requests. With candidate_id, one candidate in full.", integration).
		offer(TierRead).calling(protocol.MethodIntegrationList, protocol.MethodIntegrationShow),
	opOf("aether_integration_decide", "Decide delivery", ScopeControl, true, "call aether_integration for candidate and request ids",
		"Use this when the user approves or rejects a candidate's delivery request, or delivers an approved one: deliver pushes the candidate to its target branch, which cannot be taken back.", integrationDecide).
		enum("action", "approve", "reject", "deliver").hints(true, true).offer(TierConfirmed).
		calling(protocol.MethodIntegrationShow, protocol.MethodIntegrationDecide, protocol.MethodIntegrationDeliver),
}

// candidateRow keeps the target ref, the delivery request and its receipt,
// which the integrator agent wrote, under untrusted.
type candidateRow struct {
	CandidateID string                  `json:"candidate_id"`
	State       protocol.CandidateState `json:"state"`
	Revision    string                  `json:"candidate_revision,omitempty"`
	Request     *requestRow             `json:"delivery_request,omitempty"`
	Delivered   bool                    `json:"delivered"`
	ExpiresAt   string                  `json:"expires_at"`
	Untrusted   candidateText           `json:"untrusted"`
}

type requestRow struct {
	RequestID string                  `json:"request_id"`
	Action    protocol.DeliveryAction `json:"action"`
	State     protocol.DeliveryState  `json:"state"`
}

type candidateText struct {
	TargetRef string                    `json:"target_ref"`
	Request   *protocol.DeliveryRequest `json:"delivery_request,omitempty"`
	Receipt   *protocol.DeliveryReceipt `json:"delivery_receipt,omitempty"`
}

func candidateOf(id string, state protocol.CandidateState, targetRef, revision string, r *protocol.DeliveryRequest, rc *protocol.DeliveryReceipt, expires time.Time) candidateRow {
	row := candidateRow{CandidateID: id, State: state, Revision: revision, Delivered: rc != nil, ExpiresAt: expires.UTC().Format(time.RFC3339),
		Untrusted: candidateText{TargetRef: clip(targetRef, maxReport), Request: r, Receipt: rc}}
	if r != nil {
		row.Request = &requestRow{r.RequestID, r.Action, r.State}
	}
	return row
}

func integration(ctx context.Context, c calls, in integrationInput) (string, any, error) {
	if in.CandidateID != "" {
		var res protocol.IntegrationShowResult
		if err := c.read(ctx, protocol.MethodIntegrationShow, protocol.IntegrationShowParams{WorkspaceID: in.WorkspaceID, CandidateID: in.CandidateID}, &res); err != nil {
			return "", nil, err
		}
		cd := res.Candidate
		type verification struct {
			ID       string                      `json:"verification_id"`
			Status   protocol.VerificationStatus `json:"status"`
			ExitCode *int                        `json:"exit_code,omitempty"`
		}
		verifications := make([]verification, 0, len(cd.Verifications))
		outputs := make([]map[string]any, 0, len(cd.Verifications))
		for _, v := range cd.Verifications {
			verifications = append(verifications, verification{v.VerificationID, v.Status, v.ExitCode})
			quoted := make([]string, len(v.Argv))
			for i, arg := range v.Argv {
				quoted[i] = shellquote.Quote(arg)
			}
			outputs = append(outputs, map[string]any{"verification_id": v.VerificationID, "command": clip(strings.Join(quoted, " "), maxText), "output": clip(v.Output, maxFeedback), "error": clip(v.Error, maxFeedback)})
		}
		conflicts := make([]string, 0, min(len(cd.Conflicts), defaultRows))
		for _, path := range cd.Conflicts[:min(len(cd.Conflicts), defaultRows)] {
			conflicts = append(conflicts, clip(path, maxReport))
		}
		out := map[string]any{"candidate": candidateOf(cd.CandidateID, cd.State, cd.TargetRef, cd.CandidateRevision, cd.DeliveryRequest, cd.DeliveryReceipt, cd.ExpiresAt),
			"swarm_id": cd.MissionID, "inputs": len(cd.Inputs), "applied_inputs": cd.AppliedInputs, "conflicts": len(cd.Conflicts), "verifications": verifications,
			"untrusted": map[string]any{"verifications": outputs, "conflicts": conflicts, "error": clip(cd.Error, maxText)}}
		summary := fmt.Sprintf("Candidate %s is %s: %d verifications, %d conflicts.", cd.CandidateID, cd.State, len(verifications), len(cd.Conflicts))
		if r := cd.DeliveryRequest; r != nil {
			summary += fmt.Sprintf(" Delivery request %s (%s) is %s.", r.RequestID, r.Action, r.State)
		}
		return summary, out, nil
	}
	var res protocol.IntegrationListResult
	if err := c.read(ctx, protocol.MethodIntegrationList, protocol.IntegrationListParams{WorkspaceID: in.WorkspaceID, MissionID: in.SwarmID, Limit: maxRows}, &res); err != nil {
		return "", nil, err
	}
	rows := make([]candidateRow, 0, len(res.Candidates))
	pending := 0
	for _, cd := range res.Candidates {
		rows = append(rows, candidateOf(cd.CandidateID, cd.State, cd.TargetRef, cd.CandidateRevision, cd.DeliveryRequest, cd.DeliveryReceipt, cd.ExpiresAt))
		if cd.DeliveryRequest != nil && cd.DeliveryRequest.State == protocol.DeliveryPending {
			pending++
		}
	}
	out := map[string]any{"candidates": rows}
	summary := fmt.Sprintf("%d candidates, %d delivery requests awaiting a decision.", len(rows), pending)
	if len(rows) == maxRows {
		out["truncated"] = true
		summary = fmt.Sprintf("The newest %d candidates, %d delivery requests among them awaiting a decision; older ones are not shown.", len(rows), pending)
	}
	return summary, out, nil
}

// integrationDecide decides or delivers the request the user saw: the
// candidate's current request must be request_id, and its version is the
// one decided. A request already in the wanted state is reported as done.
func integrationDecide(ctx context.Context, c calls, in integrationDecideInput) (string, any, error) {
	var shown protocol.IntegrationShowResult
	if err := c.read(ctx, protocol.MethodIntegrationShow, protocol.IntegrationShowParams{WorkspaceID: in.WorkspaceID, CandidateID: in.CandidateID}, &shown); err != nil {
		return "", nil, err
	}
	cd := shown.Candidate
	r := cd.DeliveryRequest
	if r == nil || r.RequestID != in.RequestID {
		return "", nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf(
			"candidate %s's delivery request is no longer %s; call aether_integration with candidate_id and decide what it shows now", cd.CandidateID, in.RequestID)}
	}
	untrusted := map[string]any{"target_ref": clip(cd.TargetRef, maxReport)}
	out := map[string]any{"candidate_id": cd.CandidateID, "request_id": r.RequestID, "untrusted": untrusted}
	if in.Action == "deliver" {
		if rc := cd.DeliveryReceipt; rc != nil && rc.RequestID == r.RequestID {
			untrusted["receipt"] = rc
			return fmt.Sprintf("Candidate %s was already delivered: %s.", cd.CandidateID, rc.Result), out, nil
		}
		var res protocol.IntegrationDeliverResult
		if err := c.act(ctx, protocol.MethodIntegrationDeliver, protocol.IntegrationDeliverParams{WorkspaceID: in.WorkspaceID, CandidateID: cd.CandidateID,
			RequestID: r.RequestID, RequestVersion: r.RequestVersion}, &res); err != nil {
			return "", nil, err
		}
		out["state"], untrusted["receipt"] = res.Candidate.State, res.Candidate.DeliveryReceipt
		return fmt.Sprintf("Delivered candidate %s to its target ref; it is %s.", cd.CandidateID, res.Candidate.State), out, nil
	}
	want := protocol.DeliveryApproved
	if in.Action == "reject" {
		want = protocol.DeliveryDenied
	}
	if r.State == want {
		out["request_state"] = r.State
		return fmt.Sprintf("Candidate %s's delivery request is already %s.", cd.CandidateID, r.State), out, nil
	}
	var res protocol.IntegrationDecideResult
	if err := c.act(ctx, protocol.MethodIntegrationDecide, protocol.IntegrationDecideParams{WorkspaceID: in.WorkspaceID, CandidateID: cd.CandidateID,
		RequestID: r.RequestID, RequestVersion: r.RequestVersion, Approve: in.Action == "approve"}, &res); err != nil {
		return "", nil, err
	}
	state := protocol.DeliveryState("")
	if res.Candidate.DeliveryRequest != nil {
		state = res.Candidate.DeliveryRequest.State
	}
	out["request_state"] = state
	return fmt.Sprintf("Candidate %s's delivery request is %s.", cd.CandidateID, state), out, nil
}
