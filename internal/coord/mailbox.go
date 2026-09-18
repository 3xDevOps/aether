package coord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Send rate per run: a token bucket that lets a burst of replies through
// and then throttles to one message per refill interval.
const (
	sendBurst  = 5
	sendRefill = 5 * time.Second

	// Evidence capture may perform bounded Git and transcript I/O. Keep the
	// request finite even when the connection remains open.
	reportCaptureTimeout = 2 * time.Minute
)

const (
	inboxBurst  = 10
	inboxRefill = time.Second
)

// requestBurst/refill bound transport work independently of mutation effects.
// Every coordination method spends this budget, including idempotent retries.
const (
	requestBurst  = 30
	requestRefill = time.Second
)

// maxPeers is how many distinct runs one run may ever open a conversation
// with. Existing conversations may continue to send messages and replies.
const maxPeers = 8

var coordinationCapabilities = []string{
	protocol.MethodCoordStatus,
	protocol.MethodCoordSend,
	protocol.MethodCoordInbox,
	protocol.MethodCoordAsk,
	protocol.MethodCoordReply,
	protocol.MethodCoordReport,
}

// Status answers coord.status for run: who it is, exactly the peers it
// may message, and how many messages are waiting. A current mission
// assignment uses its server-authorized peer set; ordinary runs retain the
// radar's active/grace behavior.
func (s *Service) Status(ctx context.Context, run domain.RunID) (protocol.CoordStatusResult, *protocol.Error) {
	const method = protocol.MethodCoordStatus
	if s.cfg.Disabled {
		return protocol.CoordStatusResult{}, unavailable(method)
	}
	if !s.enterRun(run) {
		return protocol.CoordStatusResult{}, runClosing(method)
	}
	defer s.leaveRun(run)
	self, rpcErr := s.resolveRun(ctx, method, run)
	if rpcErr != nil {
		return protocol.CoordStatusResult{}, rpcErr
	}

	var assignment *protocol.CoordMissionAssignment
	var missionPeers []protocol.CoordPeer
	if s.cfg.Mission != nil {
		a, err := s.cfg.Mission.Assignment(ctx, run)
		if err != nil {
			return protocol.CoordStatusResult{}, missionRPCError(method, err)
		}
		if a.MissionID != "" {
			assignment = &a
			missionPeers, err = s.cfg.Mission.Peers(ctx, run)
			if err != nil {
				return protocol.CoordStatusResult{}, missionRPCError(method, err)
			}
		}
	}
	var radarPeers []authorizedPeer
	var radarTotal int
	var radarTruncated bool
	if assignment == nil {
		var radarErr error
		radarPeers, radarTotal, radarTruncated, radarErr = s.radar.authorizedSetBounded(ctx, run, protocol.CoordMaxStatusPeers)
		if radarErr != nil {
			return protocol.CoordStatusResult{}, internalError(method, radarErr)
		}
	}

	peers := make([]protocol.CoordPeer, 0, protocol.CoordMaxStatusPeers)
	seen := make(map[domain.RunID]int, len(missionPeers)+len(radarPeers))
	appendPeer := func(peer protocol.CoordPeer) {
		id := domain.RunID(peer.RunID)
		if id == "" || id == run {
			return
		}
		if _, ok := seen[id]; ok {
			// MissionService and the radar each return a bounded unique set;
			// a duplicate keeps the first current-authority entry.
			return
		}
		if len(peers) >= protocol.CoordMaxStatusPeers {
			return
		}
		seen[id] = len(peers)
		peers = append(peers, peer)
	}
	missionTotal := 0
	for _, peer := range missionPeers {
		if target, err := s.cfg.Store.GetRun(ctx, domain.RunID(peer.RunID)); err != nil ||
			target == nil || target.Status.Terminal() {
			continue
		}
		missionTotal++
		peer.State = protocol.CoordPeerMission
		appendPeer(s.decoratePeer(ctx, peer))
	}
	for _, p := range radarPeers {
		task, taskBytes, taskTruncated := "", 0, false
		memberID := ""
		if r, gerr := s.cfg.Store.GetRun(ctx, p.run); gerr == nil && r != nil {
			memberID = string(r.MemberID)
			task, taskBytes, taskTruncated = boundStatusText(r.Task, protocol.CoordMaxStatusTaskBytes)
		}
		files := make([]string, 0, minStatusLen(len(p.files), protocol.CoordMaxStatusFiles))
		for _, file := range p.files {
			if len(files) == protocol.CoordMaxStatusFiles {
				break
			}
			path, _, _ := boundStatusText(file, protocol.CoordMaxStatusPathBytes)
			files = append(files, path)
		}
		peer := protocol.CoordPeer{
			RunID: string(p.run), MemberID: memberID, Task: task, TaskBytes: taskBytes,
			TaskTruncated: taskTruncated, Files: files, FileTotal: len(p.files),
			FilesTruncated: len(p.files) > len(files), State: p.state,
		}
		if !p.expiry.IsZero() {
			peer.ExpiresAt = p.expiry.UTC().Format(time.RFC3339)
		}
		appendPeer(peer)
	}
	unread, err := s.cfg.Mail.CountUnackedRunMessages(ctx, run)
	if err != nil {
		return protocol.CoordStatusResult{}, internalError(method, err)
	}
	task, taskBytes, taskTruncated := boundStatusText(self.Task, protocol.CoordMaxStatusTaskBytes)
	capabilities := coordinationCapabilities
	total, truncated := radarTotal, radarTruncated
	if assignment != nil {
		capabilities = assignment.Capabilities
		total = missionTotal
		truncated = missionTotal > protocol.CoordMaxStatusPeers
	}
	return protocol.CoordStatusResult{
		WireVersion: protocol.CoordWireVersion, RunID: string(self.ID),
		WorkspaceID: string(self.WorkspaceID), MemberID: string(self.MemberID),
		Task: task, TaskBytes: taskBytes, TaskTruncated: taskTruncated,
		Assignment: assignment, Peers: peers, PeerTotal: total,
		PeersTruncated: truncated, Unread: unread,
		Capabilities: append([]string(nil), capabilities...),
	}, nil
}

func (s *Service) decoratePeer(ctx context.Context, peer protocol.CoordPeer) protocol.CoordPeer {
	if peer.TaskBytes == 0 && peer.Task != "" {
		peer.Task, peer.TaskBytes, peer.TaskTruncated =
			boundStatusText(peer.Task, protocol.CoordMaxStatusTaskBytes)
	}
	if peer.MemberID == "" {
		if r, err := s.cfg.Store.GetRun(ctx, domain.RunID(peer.RunID)); err == nil && r != nil {
			peer.MemberID = string(r.MemberID)
			if peer.Task == "" {
				peer.Task, peer.TaskBytes, peer.TaskTruncated =
					boundStatusText(r.Task, protocol.CoordMaxStatusTaskBytes)
			}
		}
	}
	return peer
}

func missionRPCError(method string, err error) *protocol.Error {
	if err == nil {
		return nil
	}
	var rpcErr *protocol.Error
	if errors.As(err, &rpcErr) && rpcErr != nil {
		copyErr := *rpcErr
		if copyErr.Message == "" {
			copyErr.Message = method + ": mission request failed"
		}
		return &copyErr
	}
	code := protocol.CodeInternal
	switch {
	case errors.Is(err, store.ErrMissionStale), errors.Is(err, store.ErrMissionTakeover):
		code = protocol.CodeDenied
	case errors.Is(err, store.ErrMissionLimit), errors.Is(err, store.ErrMissionNotReady):
		code = protocol.CodeConflict
	}
	return &protocol.Error{Code: code, Message: method + ": " + err.Error()}
}
func (s *Service) transportAllowed(run domain.RunID) bool {
	return s.spend(s.requestBuckets, run, requestBurst, requestRefill)
}

func transportRateError() *protocol.Error {
	return &protocol.Error{
		Code: protocol.CodeConflict,
		Message: fmt.Sprintf("coord: transport request rate limit exceeded (burst %d, 1 request per %ds)",
			requestBurst, int(requestRefill.Seconds())),
	}
}

func boundStatusText(value string, max int) (string, int, bool) {
	total := len(value)
	if total <= max {
		return cleanCoordText(value, max), total, false
	}
	bounded := value[:max]
	for len(bounded) > 0 && !utf8.ValidString(bounded) {
		bounded = bounded[:len(bounded)-1]
	}
	return cleanCoordText(bounded, max), total, true
}

func minStatusLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Send answers coord.send from run. The sender is the connected socket, not
// a wire parameter. Every mutation requires an idempotency key.
func (s *Service) Send(ctx context.Context, from domain.RunID, p protocol.CoordSendParams) (protocol.CoordSendResult, *protocol.Error) {
	const method = protocol.MethodCoordSend
	if s.cfg.Disabled {
		return protocol.CoordSendResult{}, unavailable(method)
	}
	if !s.enterRun(from) {
		return protocol.CoordSendResult{}, runClosing(method)
	}
	defer s.leaveRun(from)
	to := domain.RunID(p.ToRunID)
	if err := validateMessageParams(method, from, to, p.Body, p.IdempotencyKey); err != nil {
		return protocol.CoordSendResult{}, err
	}
	msg, rpcErr := s.sendMessage(ctx, method, from, to, p.Body,
		store.RunMessageKindMessage, "", p.IdempotencyKey, false)
	if rpcErr != nil {
		return protocol.CoordSendResult{}, rpcErr
	}
	return protocol.CoordSendResult{MessageID: msg.ID}, nil
}

// Ask answers coord.ask, creating a durable question whose ID is also its
// correlation ID. A retry with the same key returns the original question.
func (s *Service) Ask(ctx context.Context, from domain.RunID, p protocol.CoordAskParams) (protocol.CoordAskResult, *protocol.Error) {
	const method = protocol.MethodCoordAsk
	if s.cfg.Disabled {
		return protocol.CoordAskResult{}, unavailable(method)
	}
	if !s.enterRun(from) {
		return protocol.CoordAskResult{}, runClosing(method)
	}
	defer s.leaveRun(from)
	to := domain.RunID(p.ToRunID)
	if err := validateMessageParams(method, from, to, p.Body, p.IdempotencyKey); err != nil {
		return protocol.CoordAskResult{}, err
	}
	msg, rpcErr := s.sendMessage(ctx, method, from, to, p.Body,
		store.RunMessageKindQuestion, "", p.IdempotencyKey, false)
	if rpcErr != nil {
		return protocol.CoordAskResult{}, rpcErr
	}
	return protocol.CoordAskResult{QuestionID: msg.ID}, nil
}

// Reply answers coord.reply. It routes only to the original question sender,
// and permits the reply after normal overlap grace has expired. The question
// itself is the authorization relationship; an unrelated message cannot use
// this path to bypass radar authorization.
func (s *Service) Reply(ctx context.Context, from domain.RunID, p protocol.CoordReplyParams) (protocol.CoordReplyResult, *protocol.Error) {
	const method = protocol.MethodCoordReply
	if s.cfg.Disabled {
		return protocol.CoordReplyResult{}, unavailable(method)
	}
	if !s.enterRun(from) {
		return protocol.CoordReplyResult{}, runClosing(method)
	}
	defer s.leaveRun(from)
	if p.QuestionID == "" {
		return protocol.CoordReplyResult{}, invalidParams(method, "question_id is required")
	}
	if p.Body == "" {
		return protocol.CoordReplyResult{}, invalidParams(method, "body is required")
	}
	if len(p.Body) > protocol.CoordMaxBodyBytes {
		return protocol.CoordReplyResult{}, invalidParams(method,
			fmt.Sprintf("body exceeds %d bytes", protocol.CoordMaxBodyBytes))
	}
	if p.IdempotencyKey == "" {
		return protocol.CoordReplyResult{}, invalidParams(method, "idempotency_key is required")
	}
	if len([]byte(p.IdempotencyKey)) > protocol.CoordMaxIdempotencyKeyBytes ||
		strings.ContainsAny(p.IdempotencyKey, "\x00\r\n") {
		return protocol.CoordReplyResult{}, invalidParams(method, "idempotency_key is invalid")
	}
	question, err := s.cfg.Mail.GetQuestion(ctx, p.QuestionID)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.CoordReplyResult{}, &protocol.Error{
			Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: unknown question %s", method, p.QuestionID),
		}
	}
	if err != nil {
		return protocol.CoordReplyResult{}, internalError(method, err)
	}
	if sender, serr := s.cfg.Store.GetRun(ctx, from); serr != nil {
		if errors.Is(serr, store.ErrNotFound) {
			return protocol.CoordReplyResult{}, &protocol.Error{
				Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: unknown run %s", method, from),
			}
		}
		return protocol.CoordReplyResult{}, internalError(method, serr)
	} else if sender.WorkspaceID != question.WorkspaceID {
		return protocol.CoordReplyResult{}, &protocol.Error{
			Code: protocol.CodeDenied, Message: fmt.Sprintf("%s: question %s is outside this workspace", method, p.QuestionID),
		}
	}
	if question.ToRun != from {
		return protocol.CoordReplyResult{}, &protocol.Error{
			Code:    protocol.CodeDenied,
			Message: fmt.Sprintf("%s: question %s is not addressed to run %s", method, p.QuestionID, from),
		}
	}
	if question.FromRun == "" || question.FromRun == from {
		return protocol.CoordReplyResult{}, invalidParams(method, "question_id does not identify a reply target")
	}
	msg, rpcErr := s.sendMessage(ctx, method, from, question.FromRun, p.Body,
		store.RunMessageKindReply, p.QuestionID, p.IdempotencyKey, true)
	if rpcErr != nil {
		return protocol.CoordReplyResult{}, rpcErr
	}
	return protocol.CoordReplyResult{MessageID: msg.ID}, nil
}

// Inbox answers coord.inbox for run: acknowledge the previous batch, then
// return the next one. If no batch is ready, wait_seconds performs one
// bounded server-side wait without polling.
func (s *Service) Inbox(ctx context.Context, run domain.RunID, p protocol.CoordInboxParams) (protocol.CoordInboxResult, *protocol.Error) {
	const method = protocol.MethodCoordInbox
	if s.cfg.Disabled {
		return protocol.CoordInboxResult{}, unavailable(method)
	}
	if !s.enterRun(run) {
		return protocol.CoordInboxResult{}, runClosing(method)
	}
	defer s.leaveRun(run)
	if p.WaitSeconds < 0 || p.WaitSeconds > protocol.CoordMaxInboxWaitSeconds {
		return protocol.CoordInboxResult{}, invalidParams(method,
			fmt.Sprintf("wait_seconds must be between 0 and %d", protocol.CoordMaxInboxWaitSeconds))
	}
	if !s.allowInbox(run) {
		return protocol.CoordInboxResult{}, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: fmt.Sprintf("%s: rate limit exceeded (burst %d, 1 read per %ds)",
				method, inboxBurst, int(inboxRefill.Seconds())),
		}
	}
	if _, rpcErr := s.resolveRun(ctx, method, run); rpcErr != nil {
		return protocol.CoordInboxResult{}, rpcErr
	}
	var waiter chan struct{}
	if p.WaitSeconds > 0 {
		// Register before the first read so Release cannot race with a
		// waiter that has not yet installed its wake channel.
		waiter = s.waiter(run)
		defer s.releaseWaiter(run, waiter)
	}
	msgs, token, err := s.cfg.Mail.DeliverRunMessages(ctx, run, p.AckToken, protocol.CoordMaxUnread)
	if err != nil {
		return protocol.CoordInboxResult{}, internalError(method, err)
	}
	if len(msgs) == 0 && waiter != nil {
		// Close the race between the first read and waiter registration by
		// reading once more before sleeping.
		msgs, token, err = s.cfg.Mail.DeliverRunMessages(ctx, run, "", protocol.CoordMaxUnread)
		if err != nil {
			return protocol.CoordInboxResult{}, internalError(method, err)
		}
		if len(msgs) == 0 {
			timer := time.NewTimer(time.Duration(p.WaitSeconds) * time.Second)
			select {
			case <-waiter:
			case <-timer.C:
			case <-ctx.Done():
			case <-s.serveCtx.Done():
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if ctx.Err() == nil && s.serveCtx.Err() == nil {
				msgs, token, err = s.cfg.Mail.DeliverRunMessages(ctx, run, "", protocol.CoordMaxUnread)
				if err != nil {
					return protocol.CoordInboxResult{}, internalError(method, err)
				}
			}
		}
	}
	out := make([]protocol.CoordMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, protocol.CoordMessage{
			ID:            m.ID,
			Kind:          string(m.Kind),
			CorrelationID: m.CorrelationID,
			FromRunID:     string(m.FromRun),
			Body:          m.Body,
			CreatedAt:     m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return protocol.CoordInboxResult{Messages: out, AckToken: token}, nil
}

// resolveRun loads a run, mapping the unknown case to CodeNotFound.
func (s *Service) resolveRun(ctx context.Context, method string, run domain.RunID) (*domain.Run, *protocol.Error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &protocol.Error{
			Code:    protocol.CodeNotFound,
			Message: fmt.Sprintf("%s: unknown run %s", method, run),
		}
	}
	if err != nil {
		return nil, internalError(method, err)
	}
	return r, nil
}

// CoordReport persists one bounded outcome per run. A report slot is reserved
// before capture so a second key cannot race it, and capture failure leaves
// the pending reservation retryable by that same key.
func (s *Service) CoordReport(ctx context.Context, run domain.RunID, p protocol.CoordReportParams) (protocol.CoordReportResult, *protocol.Error) {
	const method = protocol.MethodCoordReport
	if s.cfg.Disabled {
		return protocol.CoordReportResult{}, unavailable(method)
	}
	if !s.enterRun(run) {
		return protocol.CoordReportResult{}, runClosing(method)
	}
	defer s.leaveRun(run)
	if p.Outcome != protocol.CoordOutcomeSuccess && p.Outcome != protocol.CoordOutcomeFailure && p.Outcome != protocol.CoordOutcomeBlocked {
		return protocol.CoordReportResult{}, invalidParams(method, "outcome must be \"success\", \"failure\", or \"blocked\"")
	}
	if p.IdempotencyKey == "" {
		return protocol.CoordReportResult{}, invalidParams(method, "idempotency_key is required")
	}
	if len([]byte(p.IdempotencyKey)) > protocol.CoordMaxIdempotencyKeyBytes ||
		strings.ContainsAny(p.IdempotencyKey, "\x00\r\n") {
		return protocol.CoordReportResult{}, invalidParams(method, "idempotency_key is invalid")
	}
	if len([]byte(p.Summary)) > protocol.CoordMaxSummaryBytes {
		return protocol.CoordReportResult{}, invalidParams(method,
			fmt.Sprintf("summary exceeds %d bytes", protocol.CoordMaxSummaryBytes))
	}
	summary := cleanCoordText(p.Summary, protocol.CoordMaxSummaryBytes)
	if summary == "" {
		return protocol.CoordReportResult{}, invalidParams(method, "summary is required")
	}
	if len(p.EvidenceRefs) >= protocol.CoordMaxEvidenceRefs {
		return protocol.CoordReportResult{}, invalidParams(method,
			fmt.Sprintf("evidence_refs exceeds %d user entries", protocol.CoordMaxEvidenceRefs-1))
	}
	refs := make([]string, len(p.EvidenceRefs))
	for i, ref := range p.EvidenceRefs {
		if ref == "" || len([]byte(ref)) > protocol.CoordMaxEvidenceRefBytes ||
			strings.ContainsAny(ref, "\x00\r\n\t") ||
			strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) {
			return protocol.CoordReportResult{}, invalidParams(method, "evidence_refs contains an invalid reference")
		}
		refs[i] = ref
	}
	self, rpcErr := s.resolveRun(ctx, method, run)
	if rpcErr != nil {
		return protocol.CoordReportResult{}, rpcErr
	}
	existingFinalized := false
	if prior, err := s.cfg.Mail.GetCoordReportByIdempotency(ctx, run, p.IdempotencyKey); err == nil {
		existingFinalized = prior != nil && prior.State == store.CoordReportFinalized
	} else if !errors.Is(err, store.ErrNotFound) {
		return protocol.CoordReportResult{}, internalError(method, err)
	}
	missionActive := false
	missionLookupFailed := false
	if s.cfg.Mission != nil {
		assignment, err := s.cfg.Mission.Assignment(ctx, run)
		if err != nil {
			if !existingFinalized {
				return protocol.CoordReportResult{}, missionRPCError(method, err)
			}
			missionLookupFailed = true
		} else {
			missionActive = assignment.MissionID != ""
		}
		if !existingFinalized {
			if err := s.cfg.Mission.ValidateReport(ctx, run); err != nil {
				return protocol.CoordReportResult{}, missionRPCError(method, err)
			}
		}
	}
	missionReconcile := s.cfg.Mission != nil && (missionActive || missionLookupFailed)
	if s.cfg.Evidence == nil {
		return protocol.CoordReportResult{}, internalError(method, ErrNoEvidenceCapture)
	}
	lock := s.reportLock(run)
	lock.Lock()
	defer lock.Unlock()
	nextAction := "review retained evidence"
	switch p.Outcome {
	case protocol.CoordOutcomeFailure:
		nextAction = "review failure evidence and retry if authorized"
	case protocol.CoordOutcomeBlocked:
		nextAction = "resolve the blocker using the retained evidence"
	}
	report := &store.CoordReport{
		WorkspaceID: self.WorkspaceID, RunID: run, Outcome: store.CoordOutcome(p.Outcome),
		Summary: summary, NextAction: nextAction, EvidenceRefs: refs, IdempotencyKey: p.IdempotencyKey,
	}
	_, err := s.cfg.Mail.ReserveCoordReport(ctx, report)
	if errors.Is(err, store.ErrCoordReportConflict) ||
		errors.Is(err, store.ErrCoordReportIdempotencyConflict) {
		message := fmt.Sprintf("%s: run %s already has a report under another idempotency key", method, run)
		if errors.Is(err, store.ErrCoordReportIdempotencyConflict) {
			message = fmt.Sprintf("%s: idempotency_key %q was used with different report inputs", method, p.IdempotencyKey)
		}
		return protocol.CoordReportResult{}, &protocol.Error{Code: protocol.CodeConflict, Message: message}
	}
	if err != nil {
		return protocol.CoordReportResult{}, internalError(method, err)
	}
	if report.State == store.CoordReportFinalized && report.PublishedAt != nil && !missionReconcile {
		return coordReportResult(report), nil
	}

	var packet protocol.EvidencePacket
	if report.State == store.CoordReportFinalized {
		var loadErr error
		packet, loadErr = s.loadReportPacket(ctx, report)
		if loadErr != nil {
			return protocol.CoordReportResult{}, internalError(method, loadErr)
		}
	} else {
		captureCtx, cancel := context.WithTimeout(ctx, reportCaptureTimeout)
		packet, err = s.cfg.Evidence.Capture(captureCtx, evidence.Request{
			RunID: run, Origin: store.EvidenceOrigin{Kind: store.EvidenceOriginRun, ID: string(run)},
			PublicationOwner: store.EvidencePublicationOwnerCoordReport,
			Trigger:          store.EvidenceReport, Objective: self.Task, IdempotencyKey: report.IdempotencyKey,
			NextAction: report.NextAction, Provenance: "coord.report",
		})
		cancel()
		if err != nil {
			return protocol.CoordReportResult{}, internalError(method, fmt.Errorf("capture evidence: %w", err))
		}
		if packet.ID == "" {
			return protocol.CoordReportResult{}, internalError(method, errors.New("capture evidence returned an empty packet id"))
		}
		s.rememberReportPacket(report.ID, packet)
		if len(report.EvidenceRefs) >= protocol.CoordMaxEvidenceRefs {
			return protocol.CoordReportResult{}, invalidParams(method,
				fmt.Sprintf("automatic evidence reference exceeds %d entries", protocol.CoordMaxEvidenceRefs))
		}
		report.EvidenceRefs = append(report.EvidenceRefs, packet.ID)
	}
	if report.State == store.CoordReportPending {
		if _, err = s.cfg.Mail.FinalizeCoordReport(ctx, report); err != nil {
			return protocol.CoordReportResult{}, internalError(method, err)
		}
	}
	if report.State != store.CoordReportFinalized {
		return protocol.CoordReportResult{}, internalError(method, errors.New("coord report did not finalize"))
	}
	if missionReconcile {
		if err := s.cfg.Mission.ReconcileReport(ctx, run, report, packet); err != nil {
			return protocol.CoordReportResult{}, missionRPCError(method, err)
		}
	}
	if report.PublishedAt == nil {
		if rpcErr := s.publishReportEvidence(ctx, report, packet); rpcErr != nil {
			slog.Warn("coord: report publication deferred", "report_id", report.ID, "error", rpcErr)
		}
	}
	return coordReportResult(report), nil
}
func coordReportResult(report *store.CoordReport) protocol.CoordReportResult {
	if report == nil {
		return protocol.CoordReportResult{}
	}
	return protocol.CoordReportResult{
		ReportID: report.ID, Outcome: string(report.Outcome), Summary: report.Summary,
		NextAction: report.NextAction, EvidenceRef: lastEvidenceRef(report.EvidenceRefs),
		EvidenceRefs: append([]string(nil), report.EvidenceRefs...),
	}
}
func lastEvidenceRef(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return refs[len(refs)-1]
}

func (s *Service) loadReportPacket(ctx context.Context, report *store.CoordReport) (protocol.EvidencePacket, error) {
	s.mu.Lock()
	cached, ok := s.reportPackets[report.ID]
	s.mu.Unlock()
	if ok {
		return cached, nil
	}
	if s.cfg.EvidencePackets == nil {
		return protocol.EvidencePacket{}, errors.New("coord: evidence packet lookup is not attached")
	}
	id := lastEvidenceRef(report.EvidenceRefs)
	if id == "" {
		return protocol.EvidencePacket{}, errors.New("coord: finalized report has no evidence packet reference")
	}
	packet, err := s.cfg.EvidencePackets.GetEvidencePacket(ctx, id)
	if err != nil {
		return protocol.EvidencePacket{}, fmt.Errorf("load evidence packet %s: %w", id, err)
	}
	if packet == nil {
		return protocol.EvidencePacket{}, fmt.Errorf("load evidence packet %s: empty packet", id)
	}
	out := protocol.EvidencePacketFromStore(packet)
	s.rememberReportPacket(report.ID, out)
	return out, nil
}

func (s *Service) rememberReportPacket(id string, packet protocol.EvidencePacket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reportPackets == nil {
		s.reportPackets = make(map[string]protocol.EvidencePacket)
	}
	s.reportPackets[id] = packet
}

// publishReportEvidence appends the deterministic evidence event first, then
// marks the durable outbox row published. A committed-but-returned-error
// append is reconciled by the event bus's ID lookup on the next attempt.
func (s *Service) publishReportEvidence(ctx context.Context, report *store.CoordReport, packet protocol.EvidencePacket) error {
	workspaceID := domain.WorkspaceID(packet.WorkspaceID)
	if workspaceID == "" {
		workspaceID = report.WorkspaceID
	}
	runID := domain.RunID(packet.RunID)
	if runID == "" {
		runID = report.RunID
	}
	eventTime := s.now()
	if report.FinalizedAt != nil {
		eventTime = *report.FinalizedAt
	}
	// The report projection is the existing timeline surface. Keep all
	// fields bounded by the store validation and never put a run/server
	// origin in the human ActorID slot.
	_, err := s.cfg.Bus.Publish(ctx, events.Event{
		ID: store.CoordReportEventID(report.ID), Time: eventTime,
		WorkspaceID: workspaceID, RunID: runID,
		Payload: events.TimelinePayload{
			Kind: events.TimelineReport, ReportID: report.ID,
			Outcome: string(report.Outcome), Summary: report.Summary,
			NextAction: report.NextAction, EvidenceRefs: append([]string(nil), report.EvidenceRefs...),
		},
	})
	if err != nil && !errors.Is(err, events.ErrEventAlreadyExists) {
		return fmt.Errorf("publish report timeline: %w", err)
	}
	// Preserve the evidence packet projection consumed by existing activity
	// subscribers. It has its own deterministic identity and is published
	// before the shared report outbox row is marked complete.
	actor := domain.MemberID("")
	if packet.Origin.Kind == protocol.EvidenceOriginHuman {
		actor = domain.MemberID(packet.Origin.ID)
	}
	unavailable, truncated := 0, 0
	for _, source := range packet.Sources {
		if !source.Available {
			unavailable++
		}
		if source.Truncated {
			truncated++
		}
	}
	_, err = s.cfg.Bus.Publish(ctx, events.Event{
		ID: "coord-report-evidence:" + report.ID, Time: eventTime,
		WorkspaceID: workspaceID, RunID: runID, ActorID: actor,
		Payload: events.EvidencePacketPayload{
			PacketID: packet.ID, WorkspaceID: workspaceID, RunID: runID,
			Origin:    events.EvidenceOriginPayload{Kind: string(packet.Origin.Kind), ID: packet.Origin.ID},
			CreatorID: domain.MemberID(packet.CreatorID), Trigger: string(packet.Trigger),
			EventBoundary: packet.EventBoundary, ExpiresAt: packet.ExpiresAt,
			ExpiredAt: packet.ExpiredAt, Availability: string(packet.Availability),
			ChangedFileCount: len(packet.ChangedFiles), SourceCount: len(packet.Sources),
			UnavailableSourceCount: unavailable, TruncatedSourceCount: truncated,
			UnresolvedFactCount: len(packet.UnresolvedFacts),
		},
	})
	if err != nil && !errors.Is(err, events.ErrEventAlreadyExists) {
		return fmt.Errorf("publish evidence packet: %w", err)
	}
	eventID := store.CoordReportEventID(report.ID)
	if err := s.cfg.Mail.MarkCoordReportPublished(ctx, report.ID, eventID); err != nil {
		return fmt.Errorf("mark report published: %w", err)
	}
	now := s.now()
	report.PublishedAt = &now
	s.mu.Lock()
	delete(s.reportPackets, report.ID)
	s.mu.Unlock()
	return nil
}

const coordOutboxPageSize = 32

// retryOutbox is service-lifetime work rather than startup recovery. Event
// log outages must not prevent the coordination socket from starting, and a
// poison row must not prevent later rows in a bounded page from publishing.
func (s *Service) retryOutbox() {
	backoff := time.Second
	for {
		pending, progressed, err := s.drainOutboxPage(s.serveCtx)
		if err != nil {
			slog.Warn("coord outbox drain failed", "error", err)
			if !s.waitOutbox(backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if pending {
			// Yield between bounded pages so a backlog cannot starve the
			// overlap consumer or a fresh audit row.
			if progressed {
				if !s.waitOutbox(10 * time.Millisecond) {
					return
				}
			} else if !s.waitOutbox(time.Second) {
				return
			}
			continue
		}
		if !s.waitOutbox(time.Second) {
			return
		}
	}
}

func (s *Service) waitOutbox(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.serveCtx.Done():
		return false
	}
}

func (s *Service) drainOutboxPage(ctx context.Context) (pending, progressed bool, err error) {
	reports, err := s.listPendingReports(ctx)
	if err != nil {
		return false, false, err
	}
	pending = len(reports) > 0
	for _, pub := range reports {
		if pub == nil {
			continue
		}
		report, rerr := s.cfg.Mail.GetCoordReport(ctx, pub.ReportID)
		if rerr != nil {
			s.recordReportFailure(ctx, pub, rerr, false)
			pending = true
			continue
		}
		packet, perr := s.loadReportPacket(ctx, report)
		if perr != nil {
			s.recordReportFailure(ctx, pub, perr, false)
			pending = true
			continue
		}
		if s.cfg.Mission != nil {
			if rerr := s.cfg.Mission.ReconcileReport(ctx, report.RunID, report, packet); rerr != nil {
				s.recordReportFailure(ctx, pub, rerr, false)
				pending = true
				continue
			}
		}
		if perr := s.publishReportEvidence(ctx, report, packet); perr != nil {
			permanent := errors.Is(perr, store.ErrCoordReportPublicationConflict) ||
				errors.Is(perr, events.ErrEventIDConflict)
			s.recordReportFailure(ctx, pub, perr, permanent)
			pending = true
			continue
		}
		progressed = true
	}
	if audit, ok := s.cfg.Mail.(store.CoordAuditStore); ok {
		rows, aerr := s.listPendingAudits(ctx, audit)
		if aerr != nil {
			return true, progressed, aerr
		}
		pending = pending || len(rows) > 0
		for _, pub := range rows {
			if pub == nil {
				continue
			}
			if aerr := s.publishCoordAudit(ctx, audit, pub); aerr != nil {
				permanent := errors.Is(aerr, store.ErrCoordAuditPublicationConflict) ||
					errors.Is(aerr, events.ErrEventIDConflict)
				s.recordAuditFailure(ctx, audit, pub, aerr, permanent)
				pending = true
				continue
			}
			progressed = true
		}
	}
	return pending, progressed, nil
}

func (s *Service) listPendingReports(ctx context.Context) ([]*store.CoordReportPublication, error) {
	if cursors, ok := s.cfg.Mail.(store.CoordOutboxCursorStore); ok {
		rows, err := cursors.ListPendingCoordReportPublicationsAfter(ctx, s.reportCursor, coordOutboxPageSize)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 && !s.reportCursor.CreatedAt.IsZero() {
			s.reportCursor = store.CoordOutboxCursor{}
			rows, err = cursors.ListPendingCoordReportPublicationsAfter(ctx, s.reportCursor, coordOutboxPageSize)
			if err != nil {
				return nil, err
			}
		}
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			s.reportCursor = store.CoordOutboxCursor{CreatedAt: last.CreatedAt, ID: last.ReportID}
		}
		return rows, nil
	}
	return s.cfg.Mail.ListPendingCoordReportPublications(ctx, coordOutboxPageSize)
}

func (s *Service) listPendingAudits(ctx context.Context, audit store.CoordAuditStore) ([]*store.CoordAuditPublication, error) {
	if cursors, ok := audit.(store.CoordOutboxCursorStore); ok {
		rows, err := cursors.ListPendingCoordAuditPublicationsAfter(ctx, s.auditCursor, coordOutboxPageSize)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 && !s.auditCursor.CreatedAt.IsZero() {
			s.auditCursor = store.CoordOutboxCursor{}
			rows, err = cursors.ListPendingCoordAuditPublicationsAfter(ctx, s.auditCursor, coordOutboxPageSize)
			if err != nil {
				return nil, err
			}
		}
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			s.auditCursor = store.CoordOutboxCursor{CreatedAt: last.CreatedAt, ID: last.MessageID}
		}
		return rows, nil
	}
	return audit.ListPendingCoordAuditPublications(ctx, coordOutboxPageSize)
}

func retryDelay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 5 {
		attempts = 5
	}
	return time.Second << attempts
}

func (s *Service) recordReportFailure(ctx context.Context, pub *store.CoordReportPublication, err error, permanent bool) {
	retry, ok := s.cfg.Mail.(store.CoordOutboxRetryStore)
	if !ok {
		return
	}
	next := time.Now().UTC().Add(retryDelay(pub.Attempts))
	if permanent {
		next = time.Time{}
	}
	if rerr := retry.RecordCoordReportPublicationFailure(ctx, pub.ReportID, err.Error(), next, permanent); rerr != nil {
		slog.Warn("coord report outbox failure state unavailable", "report_id", pub.ReportID, "error", rerr)
	}
}

func (s *Service) recordAuditFailure(ctx context.Context, audit store.CoordAuditStore, pub *store.CoordAuditPublication, err error, permanent bool) {
	retry, ok := audit.(store.CoordOutboxRetryStore)
	if !ok {
		return
	}
	next := time.Now().UTC().Add(retryDelay(pub.Attempts))
	if permanent {
		next = time.Time{}
	}
	if rerr := retry.RecordCoordAuditPublicationFailure(ctx, pub.MessageID, err.Error(), next, permanent); rerr != nil {
		slog.Warn("coord audit outbox failure state unavailable", "message_id", pub.MessageID, "error", rerr)
	}
}

func (s *Service) publishCoordAudit(ctx context.Context, outbox store.CoordAuditStore, pub *store.CoordAuditPublication) error {
	if pub == nil || pub.MessageID == "" {
		return store.ErrCoordAuditPublicationConflict
	}
	eventID := store.CoordAuditEventID(pub.MessageID)
	if pub.EventID != eventID {
		return store.ErrCoordAuditPublicationConflict
	}
	_, err := s.cfg.Bus.Publish(ctx, events.Event{
		ID: eventID, Time: pub.CreatedAt, WorkspaceID: pub.WorkspaceID, RunID: pub.FromRun,
		// Coordination is a run-originated protocol event, not a human
		// action. Never resolve the mutable current owner for attribution.
		ActorID: "",
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: fmt.Sprintf("coordination message to run %s: %s", pub.ToRun, pub.Body),
		},
	})
	if err != nil && !errors.Is(err, events.ErrEventAlreadyExists) {
		return err
	}
	return outbox.MarkCoordAuditPublished(ctx, pub.MessageID, eventID)
}

// sendMessage is shared by send, ask, and reply. For replies, correlated is
// true and radar authorization is intentionally skipped only after the
// question ownership check in Reply.
type peerMessageStore interface {
	AppendRunMessageWithPeer(context.Context, *store.RunMessage, int, int, bool) (bool, error)
}

func appendRunMessage(ctx context.Context, mail store.MessageStore, msg *store.RunMessage, openPeer bool) (bool, error) {
	if atomic, ok := mail.(peerMessageStore); ok {
		return atomic.AppendRunMessageWithPeer(ctx, msg, protocol.CoordMaxUnread, maxPeers, openPeer)
	}
	// Compatibility for narrow test/integration stores that predate the
	// durable peer seam. The production DB implements the atomic method.
	wasEmpty := msg.ID == ""
	if err := mail.AppendRunMessage(ctx, msg, protocol.CoordMaxUnread); err != nil {
		return false, err
	}
	return wasEmpty && msg.ID != "", nil
}

func (s *Service) sendMessage(ctx context.Context, method string, from, to domain.RunID, body string,
	kind store.RunMessageKind, correlation, idempotency string, correlated bool) (*store.RunMessage, *protocol.Error) {
	// The transport budget is charged by the public method before this
	// helper. Retries still avoid radar refresh and mutation work here.
	if prior, err := s.cfg.Mail.GetRunMessageByIdempotency(ctx, from, idempotency); err == nil {
		if !coordMessageMatches(prior, to, body, kind, correlation) {
			return nil, &protocol.Error{
				Code:    protocol.CodeConflict,
				Message: fmt.Sprintf("%s: idempotency_key %q was used with different message inputs", method, idempotency),
			}
		}
		return prior, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, internalError(method, err)
	}
	if !s.allow(from) {
		return nil, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: fmt.Sprintf("%s: rate limit exceeded (burst %d, 1 message per %ds)",
				method, sendBurst, int(sendRefill.Seconds())),
		}
	}
	sender, rpcErr := s.resolveRun(ctx, method, from)
	if rpcErr != nil {
		return nil, rpcErr
	}
	target, rpcErr := s.resolveRun(ctx, method, to)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if target.Status.Terminal() {
		return nil, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: fmt.Sprintf("%s: run %s has finished", method, to),
		}
	}
	if !correlated {
		missionAuthorized := false
		missionActive := false
		if s.cfg.Mission != nil {
			assignment, err := s.cfg.Mission.Assignment(ctx, from)
			if err != nil {
				return nil, missionRPCError(method, err)
			}
			missionActive = assignment.MissionID != ""
			if missionActive {
				peers, err := s.cfg.Mission.Peers(ctx, from)
				if err != nil {
					return nil, missionRPCError(method, err)
				}
				for _, peer := range peers {
					if target.WorkspaceID == sender.WorkspaceID && domain.RunID(peer.RunID) == to {
						missionAuthorized = true
						break
					}
				}
			}
		}
		if missionActive && !missionAuthorized {
			return nil, &protocol.Error{
				Code:    protocol.CodeDenied,
				Message: fmt.Sprintf("%s: run %s is not an authorized mission peer of run %s", method, to, from),
			}
		}
		if !missionActive && !missionAuthorized {
			peer, err := s.radar.authorized(ctx, from, to)
			if err != nil {
				return nil, internalError(method, err)
			}
			if peer.state == "" {
				return nil, &protocol.Error{
					Code:    protocol.CodeDenied,
					Message: fmt.Sprintf("%s: run %s is not an authorized peer of run %s", method, to, from),
				}
			}
		}
	}
	msg := &store.RunMessage{
		WorkspaceID: sender.WorkspaceID, FromRun: from, ToRun: to, Body: body,
		Kind: kind, CorrelationID: correlation, IdempotencyKey: idempotency,
	}
	created, err := appendRunMessage(ctx, s.cfg.Mail, msg, !correlated)
	if errors.Is(err, store.ErrIdempotencyConflict) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: fmt.Sprintf("%s: idempotency_key %q was used for another message kind", method, idempotency),
		}
	}
	if errors.Is(err, store.ErrCoordPeerLimit) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: fmt.Sprintf("%s: run %s has reached its limit of %d coordination peers", method, from, maxPeers),
		}
	}
	if errors.Is(err, store.ErrInboxFull) {
		return nil, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: fmt.Sprintf("%s: run %s inbox is full (%d unacknowledged messages)",
				method, to, protocol.CoordMaxUnread),
		}
	}
	if err != nil {
		return nil, internalError(method, err)
	}
	if created {
		s.wakeInbox(msg.ToRun)
		if audit, ok := s.cfg.Mail.(store.CoordAuditStore); ok {
			if pub, aerr := audit.GetCoordAuditPublication(ctx, msg.ID); aerr == nil {
				if pubErr := s.publishCoordAudit(ctx, audit, pub); pubErr != nil {
					slog.Warn("coord: audit publication deferred", "message_id", msg.ID, "error", pubErr)
				}
			} else if !errors.Is(aerr, store.ErrNotFound) {
				slog.Warn("coord: audit outbox lookup failed", "message_id", msg.ID, "error", aerr)
			}
		}
	}
	return msg, nil
}
func coordMessageMatches(prior *store.RunMessage, to domain.RunID, body string,
	kind store.RunMessageKind, correlation string) bool {
	if prior == nil || prior.ToRun != to || prior.Body != body || prior.Kind != kind {
		return false
	}
	return correlation == "" || prior.CorrelationID == correlation
}

func validateMessageParams(method string, from, to domain.RunID, body, key string) *protocol.Error {
	switch {
	case to == "":
		return invalidParams(method, "to_run_id is required")
	case to == from:
		return invalidParams(method, "a run cannot message itself")
	case body == "":
		return invalidParams(method, "body is required")
	case len(body) > protocol.CoordMaxBodyBytes:
		return invalidParams(method, fmt.Sprintf("body exceeds %d bytes", protocol.CoordMaxBodyBytes))
	case key == "":
		return invalidParams(method, "idempotency_key is required")
	case len([]byte(key)) > protocol.CoordMaxIdempotencyKeyBytes:
		return invalidParams(method, fmt.Sprintf("idempotency_key exceeds %d bytes", protocol.CoordMaxIdempotencyKeyBytes))
	case strings.ContainsAny(key, "\x00\r\n"):
		return invalidParams(method, "idempotency_key contains control characters")
	default:
		return nil
	}
}

type inboxWaiter struct {
	ch   chan struct{}
	refs int
}

func (s *Service) waiter(run domain.RunID) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if waiter := s.inboxWaiters[run]; waiter != nil {
		waiter.refs++
		return waiter.ch
	}
	waiter := &inboxWaiter{ch: make(chan struct{}), refs: 1}
	s.inboxWaiters[run] = waiter
	return waiter.ch
}

func (s *Service) releaseWaiter(run domain.RunID, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter := s.inboxWaiters[run]
	if waiter == nil || waiter.ch != ch {
		return
	}
	waiter.refs--
	if waiter.refs <= 0 {
		delete(s.inboxWaiters, run)
	}
}

func (s *Service) wakeInbox(run domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if waiter := s.inboxWaiters[run]; waiter != nil {
		close(waiter.ch)
		delete(s.inboxWaiters, run)
	}
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (s *Service) allow(run domain.RunID) bool {
	return s.spend(s.buckets, run, sendBurst, sendRefill)
}

func (s *Service) allowInbox(run domain.RunID) bool {
	return s.spend(s.inboxBuckets, run, inboxBurst, inboxRefill)
}

func (s *Service) spend(m map[domain.RunID]*bucket, run domain.RunID, burst float64, refill time.Duration) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	b := m[run]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		m[run] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()/refill.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func invalidParams(method, msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: method + ": " + msg}
}

func internalError(method string, err error) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInternal, Message: method + ": " + err.Error()}
}

func cleanCoordText(text string, maxBytes int) string {
	text = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || !unicode.IsControl(r) {
			return r
		}
		return ' '
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	for len([]byte(text)) > maxBytes {
		runes := []rune(text)
		if len(runes) == 0 {
			return ""
		}
		text = string(runes[:len(runes)-1])
	}
	return text
}
