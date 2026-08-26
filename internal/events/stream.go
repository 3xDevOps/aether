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

// StreamWire pumps a subscription onto write in wire form until the
// subscription ends (nil), the buffer dropped (ErrDropped), or a write or
// encode fails. Framing belongs to the caller; ordering belongs here.
func StreamWire(sub Subscription, write func(protocol.Event) error) error {
	for ev := range sub.Events() {
		// Check for drops before writing: an event already dequeued was
		// dequeued before any drop, so ending the stream pre-write leaves
		// the client's cursor pre-gap and its replay resubscription
		// recovers every dropped event. Checking after the write would let
		// one post-gap event advance the cursor past the loss.
		if sub.Dropped() > 0 {
			return ErrDropped
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
