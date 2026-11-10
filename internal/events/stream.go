package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// ErrDropped ends a wire stream whose subscriber buffer overflowed. The
// transport decides how to say so; the client's recovery is the same
// either way - resubscribe with replay from its last seen seq.
var ErrDropped = errors.New("events: subscriber buffer dropped events")

// SubscribeWire opens a subscription from a client's SubscribeRequest.
// Both transports that stream the bus - the SSH events subsystem and the
// dashboard's WebSocket - negotiate with the same request, so they build
// the same options and map a failure to the same wire error here rather
// than each in their own words.
func SubscribeWire(ctx context.Context, bus Bus, req protocol.SubscribeRequest) (Subscription, *protocol.Error) {
	types := make([]Type, 0, len(req.Types))
	for _, t := range req.Types {
		types = append(types, Type(t))
	}
	sub, err := bus.Subscribe(ctx, SubscribeOptions{
		Filter: Filter{
			Workspace: domain.WorkspaceID(req.WorkspaceID),
			Run:       domain.RunID(req.RunID),
			Types:     types,
		},
		Replay:   req.Replay,
		AfterSeq: req.AfterSeq,
	})
	if err != nil {
		code := protocol.CodeInternal
		if errors.Is(err, ErrNoLog) {
			code = protocol.CodeUnavailable
		}
		return nil, &protocol.Error{Code: code, Message: err.Error()}
	}
	return sub, nil
}

// private is a payload only one member may be streamed.
type private interface {
	Recipient() domain.MemberID
}

// repoScoped is a payload about one repository of a run, published under
// the run's workspace. For an extra repository of a multi-repo run it names
// that repository's workspace, which the member must reach too.
type repoScoped interface {
	RepoWorkspace() domain.WorkspaceID
}

// memberNaming is a payload about members, which a reader sees only when
// they see each of them and the event's actor. An empty id is a member
// nobody but a super admin sees.
type memberNaming interface {
	NamedMembers() []domain.MemberID
}

// Scope is what one reader may be shown. Workspace reports whether it
// reaches a workspace. Run, when set, reports whether it reaches every
// workspace of a run, its extra repositories' included. Sees, when set,
// reports whether it sees a member.
type Scope struct {
	Workspace func(domain.WorkspaceID) bool
	Run       func(domain.RunID) bool
	Sees      func(domain.MemberID) bool
}

// StreamWire pumps a subscription onto write in wire form, until the
// subscription ends (nil), the buffer dropped (ErrDropped), or a write or
// encode fails. A private event goes to its recipient alone, a ServerScope
// event to everyone who sees the members it names, and any other event
// only when it is Reachable. Framing belongs to the caller; ordering
// belongs here.
func StreamWire(sub Subscription, member domain.MemberID, scope Scope, write func(protocol.Event) error) error {
	for ev := range sub.Events() {
		// Check for drops before writing: an event already dequeued was
		// dequeued before any drop, so ending the stream pre-write leaves
		// the client's cursor pre-gap and its replay resubscription
		// recovers every dropped event. Checking after the write would let
		// one post-gap event advance the cursor past the loss.
		if sub.Dropped() > 0 {
			return ErrDropped
		}
		if !deliverable(ev, member, scope) {
			continue
		}
		we, err := wireEvent(ev)
		if err != nil {
			return err
		}
		if err := write(we); err != nil {
			return err
		}
	}
	return nil
}

func deliverable(ev Event, member domain.MemberID, scope Scope) bool {
	if p, ok := ev.Payload.(private); ok {
		return p.Recipient() == member
	}
	if ev.WorkspaceID == ServerScope {
		return seesNamed(ev, scope)
	}
	return Reachable(ev, scope)
}

func seesNamed(ev Event, scope Scope) bool {
	p, ok := ev.Payload.(memberNaming)
	if !ok || scope.Sees == nil {
		return true
	}
	if ev.ActorID != "" && !scope.Sees(ev.ActorID) {
		return false
	}
	for _, id := range p.NamedMembers() {
		if !scope.Sees(id) {
			return false
		}
	}
	return true
}

// Reachable reports whether scope allows ev's workspace, for an event
// about an extra repository of a run that repository's workspace, for an
// event of a run every workspace of the run, and the members ev names.
func Reachable(ev Event, scope Scope) bool {
	if !scope.Workspace(ev.WorkspaceID) {
		return false
	}
	if r, ok := ev.Payload.(repoScoped); ok && r.RepoWorkspace() != "" && !scope.Workspace(r.RepoWorkspace()) {
		return false
	}
	if ev.RunID != "" && scope.Run != nil && !scope.Run(ev.RunID) {
		return false
	}
	return seesNamed(ev, scope)
}

// wireEvent renders one event in its wire form.
func wireEvent(ev Event) (protocol.Event, error) {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return protocol.Event{}, fmt.Errorf("events: encode %s payload of event %s: %w", ev.Type, ev.ID, err)
	}
	return protocol.Event{
		ID:          ev.ID,
		Seq:         ev.Seq,
		Time:        ev.Time.UTC().Format(time.RFC3339Nano),
		WorkspaceID: string(ev.WorkspaceID),
		RunID:       string(ev.RunID),
		ActorID:     string(ev.ActorID),
		Type:        string(ev.Type),
		Payload:     payload,
	}, nil
}
