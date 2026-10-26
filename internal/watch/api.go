package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

// ErrInvalid marks a request the service refuses as asked.
var ErrInvalid = errors.New("watch: invalid request")

const maxLabelRunes = 200

// conditionsOf lists each target kind's conditions in their display
// order, and its defaults.
var conditionsOf = map[string]struct{ all, defaults []string }{
	protocol.WatchTargetRun: {
		all:      []string{protocol.WatchNeedsYou, protocol.WatchFinished, protocol.WatchPRMerged, protocol.WatchCI},
		defaults: []string{protocol.WatchNeedsYou, protocol.WatchFinished},
	},
	protocol.WatchTargetMission: {
		all:      []string{protocol.WatchNeedsYou, protocol.WatchFinished, protocol.WatchDelivered},
		defaults: []string{protocol.WatchNeedsYou, protocol.WatchFinished},
	},
	protocol.WatchTargetPR: {
		all:      []string{protocol.WatchCI, protocol.WatchReview, protocol.WatchMerged, protocol.WatchClosed},
		defaults: []string{protocol.WatchCI, protocol.WatchReview, protocol.WatchMerged},
	},
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Links into the dashboard.
func WatchLink(id string) string { return "/?page=settings&id=watches&watch=" + url.QueryEscape(id) }
func WatchesLink() string        { return "/?page=settings&id=watches" }
func RunLink(id domain.RunID) string {
	return "/?run=" + url.QueryEscape(string(id))
}
func MissionLink(id domain.MissionID) string {
	return "/?page=missions&id=" + url.QueryEscape(string(id))
}

// MissionTitle names a swarm by the first line of its objective.
func MissionTitle(m *domain.Mission) string {
	for line := range strings.Lines(m.Objective) {
		if line = strings.TrimSpace(line); line != "" {
			return clip(line, 120)
		}
	}
	return "Untitled swarm"
}

func clip(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimRight(string(runes[:limit]), " ") + "…"
}

// normalize checks conditions against kind and returns them in display
// order, the defaults when empty.
func (s *Service) normalize(kind string, conditions []string) ([]string, error) {
	set, ok := conditionsOf[kind]
	if !ok {
		return nil, invalid("target kind %q is not one of run, mission, pr", kind)
	}
	if len(conditions) == 0 {
		conditions = set.defaults
	}
	for _, c := range conditions {
		switch {
		case c == "deployed":
			return nil, invalid("Aether has no deploy signal, so it cannot watch for %q; watch finished, pr_merged or delivered instead", c)
		case !slices.Contains(set.all, c):
			return nil, invalid("condition %q does not apply to a %s; use %s", c, kind, strings.Join(set.all, ", "))
		case s.pr == nil && (kind == protocol.WatchTargetPR || c == protocol.WatchPRMerged || c == protocol.WatchCI):
			return nil, invalid("%q on a %s is not supported yet", c, kindWords[kind])
		}
	}
	var out []string
	for _, c := range set.all {
		if slices.Contains(conditions, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Service) expiry(raw string, now time.Time) (time.Time, error) {
	if raw == "" {
		return now.Add(DefaultExpiry), nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, invalid("expires_at %q is not an RFC 3339 time", raw)
	}
	if !at.After(now) {
		return time.Time{}, invalid("expires_at %s is in the past", raw)
	}
	if at.After(now.Add(MaxExpiry)) {
		return time.Time{}, invalid("expires_at %s is more than 90 days away", raw)
	}
	return at.UTC(), nil
}

func label(raw string) (string, error) {
	l := strings.TrimSpace(raw)
	if len([]rune(l)) > maxLabelRunes {
		return "", invalid("label is longer than %d characters", maxLabelRunes)
	}
	return l, nil
}

// resolve checks that target exists and member may watch it, and returns
// its workspace, its title, and the finish it already reached, which the
// new watch must not announce.
func (s *Service) resolve(ctx context.Context, member *domain.Member, t protocol.WatchTarget) (domain.WorkspaceID, string, map[string]string, error) {
	var (
		ws    domain.WorkspaceID
		title string
		fired = map[string]string{}
	)
	switch t.Kind {
	case protocol.WatchTargetRun:
		if t.RunID == "" {
			return "", "", nil, invalid("target.run_id is required")
		}
		run, err := s.store.GetRun(ctx, domain.RunID(t.RunID))
		if err != nil {
			return "", "", nil, fmt.Errorf("run %s: %w", t.RunID, err)
		}
		if run.Status.Final() {
			_, body, _ := runFinish(run)
			return "", "", nil, invalid("run %s has already ended (%s); there is nothing left to watch", t.RunID, body)
		}
		ws, title = run.WorkspaceID, push.RunTitle(run)
		if key, _, ok := runFinish(run); ok {
			fired[protocol.WatchFinished] = key
		}
	case protocol.WatchTargetMission:
		if t.MissionID == "" {
			return "", "", nil, invalid("target.mission_id is required")
		}
		m, err := s.store.GetMission(ctx, domain.MissionID(t.MissionID))
		if err != nil {
			return "", "", nil, fmt.Errorf("swarm %s: %w", t.MissionID, err)
		}
		if m.Phase == domain.MissionPhaseCancelled {
			return "", "", nil, invalid("swarm %s is cancelled; there is nothing left to watch", t.MissionID)
		}
		ws, title = m.WorkspaceID, MissionTitle(m)
		if m.Phase == domain.MissionPhaseCompleted {
			fired[protocol.WatchFinished] = string(m.Phase)
		}
	case protocol.WatchTargetPR:
		if t.Repo == "" || t.Number <= 0 {
			return "", "", nil, invalid("target.repo and target.number are required")
		}
		if t.RunID != "" {
			run, err := s.store.GetRun(ctx, domain.RunID(t.RunID))
			if err != nil {
				return "", "", nil, fmt.Errorf("run %s: %w", t.RunID, err)
			}
			ws = run.WorkspaceID
		}
		title = fmt.Sprintf("%s#%d", t.Repo, t.Number)
	default:
		return "", "", nil, invalid("target kind %q is not one of run, mission, pr", t.Kind)
	}
	if ws != "" {
		actor := permissions.Actor{ID: member.ID, Role: member.Role}
		if err := permissions.Check(permissions.View, actor, permissions.Target{Workspace: ws}); err != nil {
			return "", "", nil, fmt.Errorf("%w: %w", ErrDenied, err)
		}
	}
	return ws, title, fired, nil
}

// ErrDenied marks a target the caller may not see.
var ErrDenied = errors.New("watch: access denied")

// Create starts watching a target for member.
func (s *Service) Create(ctx context.Context, memberID domain.MemberID, p protocol.WatchCreateParams) (protocol.WatchResult, error) {
	if p.IdempotencyKey != "" {
		w, err := s.store.GetWatchByIdempotencyKey(ctx, memberID, p.IdempotencyKey)
		if err == nil {
			return s.result(ctx, w)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return protocol.WatchResult{}, err
		}
	}
	member, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	conditions, err := s.normalize(p.Target.Kind, p.Conditions)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	now := s.now()
	expires, err := s.expiry(p.ExpiresAt, now)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	lbl, err := label(p.Label)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	source := p.Source
	if source == "" {
		source = protocol.WatchSourceDashboard
	}
	if !slices.Contains([]string{protocol.WatchSourceDashboard, protocol.WatchSourceAssistant, protocol.WatchSourceMCP}, source) {
		return protocol.WatchResult{}, invalid("source %q is not one of dashboard, assistant, mcp", source)
	}
	ws, title, fired, err := s.resolve(ctx, member, p.Target)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	w := &store.Watch{
		MemberID: memberID, WorkspaceID: ws, TargetKind: p.Target.Kind, RunID: domain.RunID(p.Target.RunID),
		MissionID: domain.MissionID(p.Target.MissionID), Repo: p.Target.Repo, Number: p.Target.Number,
		TargetTitle: title, Conditions: conditions, Label: lbl, Source: source, State: protocol.WatchActive,
		Fired: fired, IdempotencyKey: p.IdempotencyKey, CreatedAt: now, UpdatedAt: now, ExpiresAt: expires,
		NextCheckAt: &now,
	}
	if p.Target.Kind == protocol.WatchTargetPR || len(prConditionsOf(conditions)) > 0 {
		prTitle, verr := s.pr.Validate(ctx, memberID, p.Target)
		switch {
		case errors.Is(verr, ErrTransient):
			w.State, w.Error, w.Failures = protocol.WatchError, verr.Error(), 1
			next := now.Add(backoff(1))
			w.NextCheckAt = &next
		case verr != nil:
			return protocol.WatchResult{}, invalid("%s", verr.Error())
		case prTitle != "" && p.Target.Kind == protocol.WatchTargetPR:
			w.TargetTitle = prTitle
		}
	}
	created, err := s.insert(ctx, w)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	if !created {
		return s.result(ctx, w)
	}
	// The first check outlives the request; if it cannot commit, the
	// next_check_at saved with the watch has the background loop retry it.
	return s.settled(context.WithoutCancel(ctx), w.ID)
}

// insert stores w unless member already has it: the watch with the same
// idempotency key, or without a key, a live watch on the same target with
// the same conditions, resumed if it was paused. It reports whether w is
// new, and otherwise replaces *w with the existing watch.
func (s *Service) insert(ctx context.Context, w *store.Watch) (bool, error) {
	if w.IdempotencyKey != "" {
		err := s.store.CreateWatch(ctx, w)
		if !errors.Is(err, store.ErrConflict) {
			return err == nil, err
		}
		first, err := s.store.GetWatchByIdempotencyKey(ctx, w.MemberID, w.IdempotencyKey)
		if err != nil {
			return false, err
		}
		*w = *first
		return false, nil
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	found, err := s.existing(ctx, w.MemberID, targetOf(w), w.Conditions)
	if err != nil {
		return false, err
	}
	if found != nil {
		*w = *found
		return false, nil
	}
	return true, s.store.CreateWatch(ctx, w)
}

// existing is member's live watch on target with conditions, resumed if it
// was paused, or nil.
func (s *Service) existing(ctx context.Context, member domain.MemberID, t protocol.WatchTarget, conditions []string) (*store.Watch, error) {
	for range 4 {
		watches, err := s.store.ListWatches(ctx, store.WatchFilter{MemberID: member, TargetKind: t.Kind, RunID: domain.RunID(t.RunID), MissionID: domain.MissionID(t.MissionID)})
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(watches, func(w *store.Watch) bool {
			return targetOf(w) == t && slices.Equal(w.Conditions, conditions) &&
				w.State != protocol.WatchDone && w.State != protocol.WatchExpired
		})
		if i < 0 {
			return nil, nil
		}
		w := watches[i]
		if w.State != protocol.WatchPaused {
			return w, nil
		}
		w.State, w.UpdatedAt = protocol.WatchActive, s.now()
		err = s.store.UpdateWatch(ctx, w)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return s.settledWatch(context.WithoutCancel(ctx), w.ID)
	}
	return nil, fmt.Errorf("watch on %s %s: changed by every concurrent update", kindWords[t.Kind], cmp.Or(t.RunID, t.MissionID, t.Repo))
}

// settled evaluates watch id once, so its result shows the target as it
// stands, and returns it.
func (s *Service) settled(ctx context.Context, id string) (protocol.WatchResult, error) {
	w, err := s.settledWatch(ctx, id)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	return s.result(ctx, w)
}

func (s *Service) settledWatch(ctx context.Context, id string) (*store.Watch, error) {
	if err := s.evaluate(ctx, id); err != nil {
		return nil, err
	}
	w, err := s.store.GetWatch(ctx, id)
	if err != nil {
		return nil, err
	}
	s.changed(ctx, w, "")
	return w, nil
}

func (s *Service) result(ctx context.Context, w *store.Watch) (protocol.WatchResult, error) {
	out, err := s.toProto(ctx, w)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	receipt, err := s.receipt(ctx, w)
	if err != nil {
		return protocol.WatchResult{}, err
	}
	return protocol.WatchResult{Watch: out, Receipt: receipt}, nil
}

var conditionWords = map[string]string{
	protocol.WatchNeedsYou: "needs you", protocol.WatchFinished: "finished", protocol.WatchPRMerged: "PR merged",
	protocol.WatchCI: "CI", protocol.WatchDelivered: "delivered", protocol.WatchReview: "review",
	protocol.WatchMerged: "merged", protocol.WatchClosed: "closed",
}

var kindWords = map[string]string{
	protocol.WatchTargetRun: "run", protocol.WatchTargetMission: "swarm", protocol.WatchTargetPR: "pull request",
}

// receipt says in one line what w watches and where its notices go.
func (s *Service) receipt(ctx context.Context, w *store.Watch) (string, error) {
	words := make([]string, 0, len(w.Conditions))
	for _, c := range w.Conditions {
		words = append(words, conditionWords[c])
	}
	line := fmt.Sprintf("Watching %s %s for: %s.", kindWords[w.TargetKind], w.TargetTitle, strings.Join(words, ", "))
	switch w.State {
	case protocol.WatchPaused:
		line = fmt.Sprintf("Paused: %s %s.", kindWords[w.TargetKind], w.TargetTitle)
	case protocol.WatchError:
		line += " Not checking now: " + w.Error + "."
	case protocol.WatchDone:
		line = fmt.Sprintf("Done: %s %s reached its final state.", kindWords[w.TargetKind], w.TargetTitle)
	case protocol.WatchExpired:
		line = fmt.Sprintf("Expired: %s %s is no longer watched.", kindWords[w.TargetKind], w.TargetTitle)
	}
	prefs, err := s.store.GetNotifyPrefs(ctx, w.MemberID)
	if err != nil {
		return "", err
	}
	switch {
	case !prefs.Push:
		line += " Push is off: in-app only."
	case s.push == nil:
		line += " Web Push is not configured on this server: in-app only."
	default:
		devices, err := s.push.Devices(ctx, w.MemberID)
		if err != nil {
			return "", err
		}
		switch devices {
		case 0:
			line += " No device has notifications turned on: in-app only."
		case 1:
			line += " Push: 1 device."
		default:
			line += fmt.Sprintf(" Push: %d devices.", devices)
		}
		if prefs.QuietTZ != "" {
			line += fmt.Sprintf(" Quiet hours %s-%s %s.", prefs.QuietStart, prefs.QuietEnd, prefs.QuietTZ)
		}
	}
	if w.State == protocol.WatchActive || w.State == protocol.WatchError || w.State == protocol.WatchPaused {
		line += " Expires " + w.ExpiresAt.Format("Jan 2") + "."
	}
	return line, nil
}

func targetOf(w *store.Watch) protocol.WatchTarget {
	return protocol.WatchTarget{Kind: w.TargetKind, RunID: string(w.RunID), MissionID: string(w.MissionID), Repo: w.Repo, Number: w.Number}
}

func stamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Service) toProto(ctx context.Context, w *store.Watch) (protocol.Watch, error) {
	out := protocol.Watch{
		ID: w.ID, MemberID: string(w.MemberID), WorkspaceID: string(w.WorkspaceID), Target: targetOf(w),
		TargetTitle: w.TargetTitle, Conditions: w.Conditions, State: w.State, Error: w.Error, Label: w.Label,
		Source: w.Source, CreatedAt: stamp(&w.CreatedAt), UpdatedAt: stamp(&w.UpdatedAt), ExpiresAt: stamp(&w.ExpiresAt),
		LastCheckAt: stamp(w.LastCheckAt), NextCheckAt: stamp(w.NextCheckAt), Link: WatchLink(w.ID),
	}
	if err := json.Unmarshal(w.Observed, &out.Observed); err != nil {
		return protocol.Watch{}, fmt.Errorf("watch %s observed: %w", w.ID, err)
	}
	last, err := s.store.ListWatchNotifications(ctx, w.ID, 1)
	if err != nil {
		return protocol.Watch{}, err
	}
	if len(last) > 0 {
		n := notificationProto(last[0])
		out.LastNotification = &n
	}
	return out, nil
}

func notificationProto(n *store.WatchNotification) protocol.WatchNotification {
	out := protocol.WatchNotification{
		ID: n.ID, WatchID: n.WatchID, Condition: n.Condition, Title: n.Title, Body: n.Body, Link: n.Link,
		CreatedAt: stamp(&n.CreatedAt), Deliveries: make([]protocol.WatchDelivery, 0, len(n.Deliveries)),
	}
	for _, d := range n.Deliveries {
		out.Deliveries = append(out.Deliveries, protocol.WatchDelivery{Channel: d.Channel, Status: d.Status, Detail: d.Detail, At: stamp(&d.At)})
	}
	return out
}

// owned is member's watch id; another member's reads as not found.
func (s *Service) owned(ctx context.Context, member domain.MemberID, id string) (*store.Watch, error) {
	if id == "" {
		return nil, invalid("watch_id is required")
	}
	w, err := s.store.GetWatch(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.MemberID != member {
		return nil, fmt.Errorf("store: watch %s: %w", id, store.ErrNotFound)
	}
	return w, nil
}

// List is member's own watches, newest first.
func (s *Service) List(ctx context.Context, member domain.MemberID, p protocol.WatchListParams) (protocol.WatchListResult, error) {
	watches, err := s.store.ListWatches(ctx, store.WatchFilter{
		MemberID: member, State: p.State, TargetKind: p.TargetKind,
		RunID: domain.RunID(p.RunID), MissionID: domain.MissionID(p.MissionID),
	})
	if err != nil {
		return protocol.WatchListResult{}, err
	}
	out := protocol.WatchListResult{Watches: make([]protocol.Watch, 0, len(watches))}
	for _, w := range watches {
		pw, err := s.toProto(ctx, w)
		if err != nil {
			return protocol.WatchListResult{}, err
		}
		out.Watches = append(out.Watches, pw)
	}
	return out, nil
}

// Get is one of member's watches and its latest notifications.
func (s *Service) Get(ctx context.Context, member domain.MemberID, id string) (protocol.WatchGetResult, error) {
	w, err := s.owned(ctx, member, id)
	if err != nil {
		return protocol.WatchGetResult{}, err
	}
	pw, err := s.toProto(ctx, w)
	if err != nil {
		return protocol.WatchGetResult{}, err
	}
	notes, err := s.store.ListWatchNotifications(ctx, id, protocol.WatchNotificationsMax)
	if err != nil {
		return protocol.WatchGetResult{}, err
	}
	out := protocol.WatchGetResult{Watch: pw, Notifications: make([]protocol.WatchNotification, 0, len(notes))}
	for _, n := range notes {
		out.Notifications = append(out.Notifications, notificationProto(n))
	}
	return out, nil
}

// Update pauses, resumes or changes one of member's watches.
func (s *Service) Update(ctx context.Context, member domain.MemberID, p protocol.WatchUpdateParams) (protocol.WatchResult, error) {
	for range 4 {
		w, err := s.owned(ctx, member, p.WatchID)
		if err != nil {
			return protocol.WatchResult{}, err
		}
		if w.State == protocol.WatchDone {
			return protocol.WatchResult{}, invalid("watch %s is done: its target reached its final state", w.ID)
		}
		now := s.now()
		if len(p.Conditions) > 0 {
			if w.Conditions, err = s.normalize(w.TargetKind, p.Conditions); err != nil {
				return protocol.WatchResult{}, err
			}
		}
		if p.ExpiresAt != "" {
			if w.ExpiresAt, err = s.expiry(p.ExpiresAt, now); err != nil {
				return protocol.WatchResult{}, err
			}
			if w.State == protocol.WatchExpired {
				w.State, w.EndedAt = protocol.WatchActive, nil
			}
		} else if w.State == protocol.WatchExpired {
			return protocol.WatchResult{}, invalid("watch %s has expired; give it a new expires_at to watch again", w.ID)
		}
		if p.Label != nil {
			if w.Label, err = label(*p.Label); err != nil {
				return protocol.WatchResult{}, err
			}
		}
		if p.Paused != nil {
			switch {
			case *p.Paused:
				w.State, w.NextCheckAt = protocol.WatchPaused, nil
			case w.State == protocol.WatchPaused:
				w.State = protocol.WatchActive
			}
		}
		w.UpdatedAt = now
		err = s.store.UpdateWatch(ctx, w)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			return protocol.WatchResult{}, err
		}
		return s.settled(ctx, w.ID)
	}
	return protocol.WatchResult{}, fmt.Errorf("watch %s: changed by every concurrent update", p.WatchID)
}

// Delete stops and removes one of member's watches.
func (s *Service) Delete(ctx context.Context, member domain.MemberID, id string) error {
	w, err := s.owned(ctx, member, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteWatch(ctx, id); err != nil {
		return err
	}
	s.changed(ctx, w, "")
	return nil
}

// Prefs is member's notification preferences.
func (s *Service) Prefs(ctx context.Context, member domain.MemberID) (protocol.NotifyPrefs, error) {
	p, err := s.store.GetNotifyPrefs(ctx, member)
	if err != nil {
		return protocol.NotifyPrefs{}, err
	}
	return s.prefsProto(ctx, member, p)
}

func (s *Service) prefsProto(ctx context.Context, member domain.MemberID, p store.NotifyPrefs) (protocol.NotifyPrefs, error) {
	out := protocol.NotifyPrefs{Push: p.Push}
	if p.QuietTZ != "" {
		out.QuietHours = &protocol.QuietHours{Start: p.QuietStart, End: p.QuietEnd, TZ: p.QuietTZ}
	}
	if s.push != nil {
		devices, err := s.push.Devices(ctx, member)
		if err != nil {
			return protocol.NotifyPrefs{}, err
		}
		out.Destinations.PushDevices = devices
	}
	return out, nil
}

// SetPrefs replaces member's notification preferences.
func (s *Service) SetPrefs(ctx context.Context, member domain.MemberID, p protocol.NotifyPrefs) (protocol.NotifyPrefs, error) {
	rec := store.NotifyPrefs{Push: p.Push}
	if q := p.QuietHours; q != nil {
		start, okStart := minutes(q.Start)
		end, okEnd := minutes(q.End)
		if !okStart || !okEnd {
			return protocol.NotifyPrefs{}, invalid("quiet_hours start and end are HH:MM, such as 22:00")
		}
		if start == end {
			return protocol.NotifyPrefs{}, invalid("quiet_hours start and end are the same time")
		}
		if q.TZ == "" {
			return protocol.NotifyPrefs{}, invalid("quiet_hours.tz is required, an IANA time zone such as Europe/Berlin")
		}
		if _, err := time.LoadLocation(q.TZ); err != nil {
			return protocol.NotifyPrefs{}, invalid("quiet_hours.tz %q is not an IANA time zone: %v", q.TZ, err)
		}
		rec.QuietStart, rec.QuietEnd, rec.QuietTZ = q.Start, q.End, q.TZ
	}
	if err := s.store.PutNotifyPrefs(ctx, member, rec); err != nil {
		return protocol.NotifyPrefs{}, err
	}
	if err := s.store.ReleaseHeldWatchPushes(ctx, member, s.now()); err != nil {
		return protocol.NotifyPrefs{}, err
	}
	s.wake()
	return s.prefsProto(ctx, member, rec)
}
