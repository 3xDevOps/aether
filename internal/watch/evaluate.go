package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

// firedSep joins the keys of a condition's notices that are current
// together, such as a run's several open questions, in Watch.Fired.
const firedSep = "\n"

// errRevoked marks a watch whose owner can no longer see its target.
var errRevoked = errors.New("access revoked")

// reading is what one evaluation found: the target as it stands, the
// notices due, and whether the target can change no further. ok is set
// when it holds a read, which a failing pull request evaluation can leave
// beside its error.
type reading struct {
	ok        bool
	title     string
	observed  protocol.WatchObserved
	notices   []Notice
	final     bool
	nextCheck time.Time
}

// evaluate reads watch id's target as it stands and commits what it
// found. A concurrent evaluation that committed first makes it read again.
// The error is a commit that failed; a target that cannot be read is
// recorded on the watch.
func (s *Service) evaluate(ctx context.Context, id string) error {
	for range 4 {
		w, err := s.store.GetWatch(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if w.State != protocol.WatchActive && w.State != protocol.WatchError || !w.ExpiresAt.After(s.now()) {
			return nil
		}
		err = s.commit(ctx, w)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("watch %s: changed by every concurrent evaluation", id)
}

func (s *Service) commit(ctx context.Context, w *store.Watch) error {
	now := s.now()
	before := *w
	before.Fired = maps.Clone(w.Fired)
	var prev protocol.WatchObserved
	if err := json.Unmarshal(w.Observed, &prev); err != nil {
		return fmt.Errorf("watch %s observed: %w", w.ID, err)
	}
	w.LastCheckAt, w.UpdatedAt = &now, now
	var notes []*store.WatchNotification
	r, readErr := s.read(ctx, w, prev)
	if errors.Is(readErr, errRevoked) {
		w.State, w.Error, w.NextCheckAt = protocol.WatchError, readErr.Error(), nil
	} else if r.ok {
		if r.title != "" {
			w.TargetTitle = r.title
		}
		observed, err := json.Marshal(r.observed)
		if err != nil {
			return err
		}
		w.Observed = observed
		if notes, err = s.notifications(ctx, w, r.notices, now); err != nil {
			return err
		}
		fired := map[string][]string{}
		for _, n := range r.notices {
			fired[n.Condition] = append(fired[n.Condition], n.Key)
		}
		for condition, keys := range fired {
			w.Fired[condition] = strings.Join(keys, firedSep)
		}
	}
	switch {
	case errors.Is(readErr, errRevoked):
	case readErr != nil:
		w.Failures++
		next := now.Add(backoff(w.Failures))
		var hint interface{ RetryAt() time.Time }
		if errors.As(readErr, &hint) && hint.RetryAt().After(next) {
			next = hint.RetryAt()
		}
		w.State, w.Error, w.NextCheckAt = protocol.WatchError, readErr.Error(), &next
	case r.final:
		w.State, w.Error, w.Failures, w.EndedAt, w.NextCheckAt = protocol.WatchDone, "", 0, &now, nil
	default:
		w.State, w.Error, w.Failures, w.NextCheckAt = protocol.WatchActive, "", 0, nil
		if !r.nextCheck.IsZero() {
			w.NextCheckAt = &r.nextCheck
		}
	}
	if len(notes) == 0 && !visiblyChanged(&before, w) && maps.Equal(before.Fired, w.Fired) &&
		(before.NextCheckAt == nil) == (w.NextCheckAt == nil) && (w.NextCheckAt == nil || w.NextCheckAt.Equal(*before.NextCheckAt)) {
		return s.store.TouchWatchCheck(ctx, w.ID, now)
	}
	recorded, err := s.store.CommitWatchEvaluation(ctx, w, notes)
	if err != nil {
		return err
	}
	for _, n := range recorded {
		s.changed(ctx, w, n.ID)
		if n.PushState != store.WatchPushNone {
			s.wake()
		}
	}
	if len(recorded) == 0 && visiblyChanged(&before, w) {
		s.changed(ctx, w, "")
	}
	return nil
}

func visiblyChanged(before, after *store.Watch) bool {
	return before.State != after.State || before.Error != after.Error || before.TargetTitle != after.TargetTitle ||
		string(before.Observed) != string(after.Observed)
}

func backoff(failures int) time.Duration {
	wait := 30 * time.Second
	for range min(failures, 10) - 1 {
		wait *= 2
	}
	return min(wait, maxBackoff)
}

// allowed checks that w's owner still exists, is approved and can view
// its workspace.
func (s *Service) allowed(ctx context.Context, w *store.Watch) error {
	m, err := s.store.GetMember(ctx, w.MemberID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: the member no longer exists", errRevoked)
	}
	if err != nil {
		return err
	}
	if m.Pending {
		return fmt.Errorf("%w: the member awaits approval", errRevoked)
	}
	if w.WorkspaceID == "" {
		return nil
	}
	if _, err := s.store.GetWorkspace(ctx, w.WorkspaceID); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: the workspace no longer exists", errRevoked)
	} else if err != nil {
		return err
	}
	actor := permissions.Actor{ID: m.ID, Role: m.Role}
	if err := permissions.Check(permissions.View, actor, permissions.Target{Workspace: w.WorkspaceID}); err != nil {
		return fmt.Errorf("%w: %w", errRevoked, err)
	}
	return nil
}

func (s *Service) read(ctx context.Context, w *store.Watch, prev protocol.WatchObserved) (reading, error) {
	if err := s.allowed(ctx, w); err != nil {
		return reading{}, err
	}
	switch w.TargetKind {
	case protocol.WatchTargetRun:
		return s.readRun(ctx, w, prev)
	case protocol.WatchTargetMission:
		return s.readMission(ctx, w)
	case protocol.WatchTargetPR:
		if s.pr == nil {
			return reading{}, errors.New("pull request watches are not supported yet")
		}
		res, err := s.pr.Evaluate(ctx, PRCheck{Member: w.MemberID, Target: targetOf(w), Conditions: w.Conditions, Previous: prev, Since: w.CreatedAt})
		if err != nil {
			return reading{}, err
		}
		return reading{ok: true, title: res.Title, observed: res.Observed, notices: res.Notices, final: res.Final, nextCheck: res.NextCheck}, nil
	}
	return reading{}, fmt.Errorf("unknown target kind %q", w.TargetKind)
}

func (s *Service) readRun(ctx context.Context, w *store.Watch, prev protocol.WatchObserved) (reading, error) {
	run, err := s.store.GetRun(ctx, w.RunID)
	if err != nil {
		return reading{}, fmt.Errorf("read run %s: %w", w.RunID, err)
	}
	r := reading{ok: true, title: push.RunTitle(run), final: run.Status.Final()}
	link := RunLink(run.ID)
	r.observed = protocol.WatchObserved{RunStatus: string(run.Status), Activity: string(run.Activity)}
	if slices.Contains(w.Conditions, protocol.WatchNeedsYou) {
		for i, n := range s.runNeeds(run) {
			if i == 0 {
				r.observed.Need = n.body
			}
			r.notices = append(r.notices, Notice{Condition: protocol.WatchNeedsYou, Key: n.key, Title: r.title, Body: n.body, Link: link})
		}
	}
	var finished string
	if key, body, ok := runFinish(run); ok {
		finished = body
		if slices.Contains(w.Conditions, protocol.WatchFinished) {
			r.notices = append(r.notices, Notice{Condition: protocol.WatchFinished, Key: key, Title: r.title, Body: body, Link: link})
		}
	}
	r.observed.Summary = cmp.Or(r.observed.Need, finished, runSummary(run))
	if prConditions := prConditionsOf(w.Conditions); len(prConditions) > 0 && s.pr != nil {
		res, err := s.pr.Evaluate(ctx, PRCheck{Member: w.MemberID, Target: targetOf(w), Conditions: prConditions, Previous: prev, Since: w.CreatedAt})
		if err != nil {
			r.notices = append(r.notices, res.Notices...)
			r.final = false
			r.observed.PRState, r.observed.HeadSHA, r.observed.Checks, r.observed.ReviewState =
				prev.PRState, prev.HeadSHA, prev.Checks, prev.ReviewState
			return r, err
		}
		r.notices = append(r.notices, res.Notices...)
		r.observed.PRState, r.observed.HeadSHA, r.observed.Checks, r.observed.ReviewState =
			res.Observed.PRState, res.Observed.HeadSHA, res.Observed.Checks, res.Observed.ReviewState
		r.final = r.final && res.Final
		r.nextCheck = res.NextCheck
	}
	return r, nil
}

func runSummary(run *domain.Run) string {
	if run.Activity != "" {
		return strings.ToUpper(string(run.Activity[:1])) + strings.ReplaceAll(string(run.Activity[1:]), "-", " ")
	}
	return strings.ToUpper(string(run.Status[:1])) + strings.ReplaceAll(string(run.Status[1:]), "-", " ")
}

type need struct{ key, body string }

var questionKinds = []string{"question", "form", "extension_ui"}

// runNeeds is what the run waits on a member for, in the Needs you
// table's order: each of its own open permission requests and questions,
// else its status when domain.Run.NeedsAttention holds. Each is a need
// of its own. A reported success is the finished condition's, not a need.
func (s *Service) runNeeds(run *domain.Run) []need {
	if !run.Status.Live() || s.runs.Paused(run.ID) {
		return nil
	}
	var out []need
	if run.MissionRole != "worker" {
		inputs := s.runs.PendingInputs(run.ID)
		for i := range inputs {
			if inputs[i].Kind == "permission" {
				out = append(out, need{key: inputKey(&inputs[i]), body: s.requestLine(run, &inputs[i], "Permission requested")})
			}
		}
		if run.Mode != domain.LaunchHeadless {
			for i := range inputs {
				if slices.Contains(questionKinds, inputs[i].Kind) {
					out = append(out, need{key: inputKey(&inputs[i]), body: s.requestLine(run, &inputs[i], "Question")})
				}
			}
		}
	}
	if len(out) > 0 || !run.NeedsAttention(false) || run.Review == domain.ReviewReady {
		return out
	}
	return []need{{key: "status:" + run.NeedKey(), body: push.AttentionBody(run)}}
}

func (s *Service) requestLine(run *domain.Run, input *domain.RunInputRequest, label string) string {
	if run.ACP {
		for _, p := range s.runs.ACPPending(run.ID) {
			if p.ID == input.ID && strings.TrimSpace(p.Title) != "" {
				return label + ": " + strings.TrimSpace(p.Title)
			}
		}
		return label
	}
	return label + ", answer in the terminal"
}

func inputKey(r *domain.RunInputRequest) string {
	return "input:" + r.SessionID + "|" + r.Kind + "|" + r.ID
}

// runFinish words how the run ended, keeping apart what the agent
// reported from what a member decided. A success the agent reported is
// keyed by when it was reported, so a later one is announced again.
func runFinish(run *domain.Run) (key, body string, ok bool) {
	reason := strings.TrimSuffix(run.Reason, "; retained container")
	switch run.Status {
	case domain.RunMerged:
		return "merged", "Closed as merged", true
	case domain.RunAbandoned:
		return "abandoned", "Closed as abandoned", true
	case domain.RunFailed:
		if reason == "" {
			return "failed", "Failed", true
		}
		return "failed", "Failed: " + reason, true
	case domain.RunInterrupted:
		return "interrupted", "Stopped", true
	}
	if run.Review == domain.ReviewReady && (run.Status == domain.RunCompleted || run.Status.Live() && run.Activity == domain.ActivityFinished) {
		at := run.ReviewChangedAt
		if at == nil {
			at = run.StatusChangedAt
		}
		key = "reported"
		if at != nil {
			key += "|" + strconv.FormatInt(at.UnixNano(), 10)
		}
		return key, "Agent reported done", true
	}
	if run.Status == domain.RunCompleted && run.Review == "" {
		return "completed", "Completed", true
	}
	return "", "", false
}

func (s *Service) readMission(ctx context.Context, w *store.Watch) (reading, error) {
	m, err := s.store.GetMission(ctx, w.MissionID)
	if err != nil {
		return reading{}, fmt.Errorf("read swarm %s: %w", w.MissionID, err)
	}
	// A completed swarm can be reopened or still deliver; only a cancelled
	// one is over.
	r := reading{ok: true, title: MissionTitle(m), final: m.Phase == domain.MissionPhaseCancelled}
	r.observed = protocol.WatchObserved{Phase: string(m.Phase)}
	link := MissionLink(m.ID)
	if slices.Contains(w.Conditions, protocol.WatchNeedsYou) {
		n, err := s.missionNeed(ctx, m)
		if err != nil {
			return reading{}, err
		}
		if n != nil {
			r.observed.Need = n.body
			r.notices = append(r.notices, Notice{Condition: protocol.WatchNeedsYou, Key: n.key, Title: r.title, Body: n.body, Link: link})
		}
	}
	var finished string
	if m.Phase.Terminal() {
		finished = "Swarm " + string(m.Phase)
		if slices.Contains(w.Conditions, protocol.WatchFinished) {
			key, err := s.missionFinish(ctx, m)
			if err != nil {
				return reading{}, err
			}
			r.notices = append(r.notices, Notice{Condition: protocol.WatchFinished, Key: key, Title: r.title, Body: finished, Link: link})
		}
	}
	var delivered string
	if slices.Contains(w.Conditions, protocol.WatchDelivered) {
		receipts, err := s.deliveries(ctx, m, w.CreatedAt)
		if err != nil {
			return reading{}, err
		}
		for _, d := range receipts {
			delivered = d.Body
			r.notices = append(r.notices, Notice{Condition: protocol.WatchDelivered, Key: d.Key, Title: r.title, Body: d.Body, Link: link})
		}
	}
	r.observed.Summary = cmp.Or(r.observed.Need, finished, delivered, "Swarm "+string(m.Phase))
	return r, nil
}

// missionFinish keys the swarm's finish by when it happened, so a swarm
// that is reopened and completes again is announced again.
func (s *Service) missionFinish(ctx context.Context, m *domain.Mission) (string, error) {
	at, err := s.store.MissionFinishedAt(ctx, m.ID)
	if err != nil {
		return "", fmt.Errorf("read swarm %s finish: %w", m.ID, err)
	}
	if at == nil {
		return string(m.Phase), nil
	}
	return string(m.Phase) + "|" + strconv.FormatInt(at.UnixNano(), 10), nil
}

// missionNeed is the swarm row of the Needs you table its current
// integrator carries: a question, or the integrator down. Each question
// is a need of its own.
func (s *Service) missionNeed(ctx context.Context, m *domain.Mission) (*need, error) {
	if m.CurrentIntegratorRunID == "" {
		return nil, nil
	}
	run, err := s.store.GetRun(ctx, m.CurrentIntegratorRunID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read swarm %s integrator: %w", m.ID, err)
	}
	item, _, err := s.swarm(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("read swarm %s attention: %w", m.ID, err)
	}
	if item == nil {
		return nil, nil
	}
	key := item.Kind + ":" + item.Since
	if item.Message != nil {
		key = item.Kind + ":" + item.Message.ID
	}
	return &need{key: key, body: item.Reason}, nil
}

type delivery struct {
	Key, Body string
	at        time.Time
}

// deliveries are the swarm's integration delivery receipts since the watch
// began, oldest first: a ref landed, or a proposal opened.
func (s *Service) deliveries(ctx context.Context, m *domain.Mission, since time.Time) ([]delivery, error) {
	receipts, err := s.store.MissionDeliveryReceipts(ctx, m.WorkspaceID, m.ID)
	if err != nil {
		return nil, fmt.Errorf("read swarm %s deliveries: %w", m.ID, err)
	}
	var out []delivery
	for candidate, raw := range receipts {
		var receipt protocol.DeliveryReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return nil, fmt.Errorf("read candidate %s delivery receipt: %w", candidate, err)
		}
		if receipt.CreatedAt.Before(since) {
			continue
		}
		body := "Delivered to " + receipt.TargetRef
		if receipt.Result == protocol.DeliveryResultProposed {
			body = "Proposed " + cmp.Or(receipt.ProposalRef, receipt.TargetRef)
		}
		out = append(out, delivery{Key: receipt.ReceiptID, Body: body, at: receipt.CreatedAt})
	}
	slices.SortFunc(out, func(a, b delivery) int { return cmp.Or(a.at.Compare(b.at), strings.Compare(a.Key, b.Key)) })
	return out, nil
}

func prConditionsOf(conditions []string) []string {
	var out []string
	for _, c := range conditions {
		if c == protocol.WatchPRMerged || c == protocol.WatchCI {
			out = append(out, c)
		}
	}
	return out
}

// notifications turns notices due into records, skipping any already
// fired for its condition: the store's uniqueness catches the rest. In-app
// is recorded delivered at once; the push row says at once what will
// happen to the push, and is updated as it resolves.
func (s *Service) notifications(ctx context.Context, w *store.Watch, notices []Notice, now time.Time) ([]*store.WatchNotification, error) {
	var out []*store.WatchNotification
	var prefs *store.NotifyPrefs
	devices := -1
	for _, n := range notices {
		if slices.Contains(strings.Split(w.Fired[n.Condition], firedSep), n.Key) {
			continue
		}
		if prefs == nil {
			p, err := s.store.GetNotifyPrefs(ctx, w.MemberID)
			if err != nil {
				return nil, err
			}
			prefs = &p
		}
		rec := &store.WatchNotification{
			Condition: n.Condition, Key: n.Key, Title: n.Title, Body: n.Body, Link: cmp.Or(n.Link, WatchLink(w.ID)),
			CreatedAt: now, PushState: store.WatchPushNone,
			Deliveries: []store.WatchDelivery{{Channel: protocol.WatchChannelInApp, Status: protocol.WatchStatusDelivered, At: now}},
		}
		d := store.WatchDelivery{Channel: protocol.WatchChannelPush, At: now}
		switch {
		case !prefs.Push:
			d.Status, d.Detail = protocol.WatchStatusDisabled, "Push is off in your notification settings"
		case s.push == nil:
			d.Status, d.Detail = protocol.WatchStatusDisabled, "Push is not configured on this server"
		default:
			if devices < 0 {
				count, err := s.push.Devices(ctx, w.MemberID)
				if err != nil {
					return nil, err
				}
				devices = count
			}
			if devices == 0 {
				d.Status, d.Detail = protocol.WatchStatusNoDestination, "No device has notifications turned on"
				break
			}
			rec.PushState, rec.PushNextAt = store.WatchPushPending, &now
			d.Status, d.Detail = protocol.WatchStatusPending, "Sending"
			if until, quiet := quietUntil(*prefs, now); quiet {
				rec.PushState, rec.PushNextAt = store.WatchPushHeld, &until
				d.Status, d.Detail = protocol.WatchStatusHeldQuiet, quietDetail(*prefs, until)
			} else if s.push.HeldUntil(w.MemberID).After(now) {
				d.Detail = dashboardHold
			}
		}
		rec.Deliveries = append(rec.Deliveries, d)
		out = append(out, rec)
	}
	return out, nil
}
