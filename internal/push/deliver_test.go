package push

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeliverTagsTheNotificationWithItsTopic(t *testing.T) {
	h := newHarness(t)
	phone := newBrowser(t)
	tags := make(chan string, 1)
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		plain, err := phone.open(body)
		if err != nil {
			t.Errorf("message does not decrypt with the subscription's keys: %v", err)
			return
		}
		var m message
		if err = json.Unmarshal(plain, &m); err != nil {
			t.Errorf("payload %q is not a message: %v", plain, err)
			return
		}
		tags <- m.Tag
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(service.Close)
	if err := h.svc.Subscribe(h.ctx, h.ada.ID, service.URL+"/send/device", phone.p256dh(), phone.authKey()); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	const topic = "wat-3m8q2k7v4c|needs_you"
	results, err := h.svc.Deliver(h.ctx, h.ada.ID, Message{Title: "Fix it", Body: "which region?", Run: "run-7k2m9q4xbd", Topic: topic})
	if err != nil || len(results) != 1 || results[0].Status != DeviceDelivered {
		t.Fatalf("Deliver = %+v, %v; want one delivered device", results, err)
	}
	if got := <-tags; got != topic {
		t.Fatalf("tag = %q, want %q, apart from the session's own notification", got, topic)
	}
}
