package push

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Device statuses of a Deliver result.
const (
	DeviceDelivered = "delivered"
	DeviceFailed    = "failed"
)

// DeviceResult is what one of a member's devices' push service said. Err
// is set when it did not take the message; a device the push service
// reports gone is unsubscribed, as every send does.
type DeviceResult struct {
	Endpoint string
	Status   string
	Err      error
}

// Message is a notification Deliver sends. URL is the dashboard path a
// click opens; Run names the run it is about, when it is about one.
// Notice keeps it shown when the run's need later ends and its
// notification is cleared. Clear quietly replaces the need shown under the
// same run, or URL when there is no run, as a need that ended is.
type Message struct {
	Title, Body, URL string
	Run              domain.RunID
	Topic            string
	Urgent           bool
	Notice           bool
	Clear            bool
}

type message struct {
	notification
	URL string `json:"url,omitempty"`
}

// Deliver sends m to every device member subscribed now, without waiting
// for them to leave a dashboard, and returns each device's result. No
// result means member has no device.
func (s *Service) Deliver(ctx context.Context, member domain.MemberID, m Message) ([]DeviceResult, error) {
	n := notification{Title: clip(m.Title, fallbackTitleRunes), Body: clip(m.Body, maxBodyRunes), Run: string(m.Run), Notice: m.Notice}
	if m.Clear {
		n.Body, n.Clear = clearedBody, true
	}
	payload, err := json.Marshal(message{notification: n, URL: m.URL})
	if err != nil {
		return nil, fmt.Errorf("push: encode notification: %w", err)
	}
	subs, err := s.store.ListPushSubscriptions(ctx, member)
	if err != nil {
		return nil, fmt.Errorf("push: list subscriptions: %w", err)
	}
	urgency := urgencyNormal
	if m.Urgent {
		urgency = urgencyHigh
	}
	topic := ""
	if m.Topic != "" {
		topic = topicOf(m.Topic)
	}
	results := make([]DeviceResult, 0, len(subs))
	for _, sub := range subs {
		r := DeviceResult{Endpoint: sub.Endpoint, Status: DeviceDelivered}
		if err := s.send(ctx, sub, payload, topic, urgency); err != nil {
			r.Status, r.Err = DeviceFailed, err
		}
		results = append(results, r)
	}
	return results, nil
}

// Devices counts the devices member subscribed.
func (s *Service) Devices(ctx context.Context, member domain.MemberID) (int, error) {
	subs, err := s.store.ListPushSubscriptions(ctx, member)
	if err != nil {
		return 0, fmt.Errorf("push: list subscriptions: %w", err)
	}
	return len(subs), nil
}

// HeldUntil is when member will have been away from every dashboard long
// enough for their devices to be told; before then the dashboard shows
// them what a push would.
func (s *Service) HeldUntil(member domain.MemberID) time.Time {
	return s.freeAt(member)
}
