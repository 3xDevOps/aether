package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMessagePrintsTheReceipt(t *testing.T) {
	after := "2026-10-25T06:00:45Z"
	for _, tc := range []struct {
		message protocol.RoomMessage
		out     string
		err     string
	}{
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageSent}, out: "message m1 delivered to run r1\n"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageSent, AgentDelivery: "queued"},
			out: "message m1 sent to run r1, queued behind the agent's current turn\n"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageQueued, DeliverAfter: &after},
			out: "message m1 queued for the member in control of run r1, who can approve or deny it; otherwise it is delivered at " + after + "\n"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageDenied, Failure: &protocol.RoomMessageFailure{Message: "denied by the controller"}},
			err: "message m1 to run r1 was denied: denied by the controller"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageNotSent}, err: "message m1 to run r1 was not sent"},
	} {
		c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunInject: {result: protocol.RunRoomPostResult{Message: tc.message}}})
		var out bytes.Buffer
		err := sendMessage(c, &out, "r1", "hi", "k-1")
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("%s message: error %v, want %q", tc.message.State, err, tc.err)
			}
			continue
		}
		if err != nil || out.String() != tc.out {
			t.Errorf("%s message: printed %q (%v), want %q", tc.message.State, out.String(), err, tc.out)
		}
	}
}

func TestRunActionsPrintTheStatusAndNameMissingRuns(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunPause: {result: struct{}{}},
		protocol.MethodRunGet:   {result: protocol.RunResult{Run: protocol.Run{ID: "r1", Status: "running", Paused: true}}},
		protocol.MethodRunKill:  {err: &protocol.Error{Code: protocol.CodeNotFound, Message: "store: not found"}},
	})
	var out bytes.Buffer
	if err := actOnRun(c, &out, protocol.MethodRunPause, "r1"); err != nil || out.String() != "run r1 paused\n" {
		t.Fatalf("pause printed %q (%v), want run r1 paused", out.String(), err)
	}
	if err := actOnRun(c, &out, protocol.MethodRunKill, "r9"); err == nil || err.Error() != "run r9 not found; aether runs lists runs" {
		t.Fatalf("kill of an unknown run = %v", err)
	}
	if strings.Contains(out.String(), "r9") {
		t.Fatalf("kill of an unknown run printed %q", out.String())
	}
}
