package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"testing"
)

func TestTerminalRecordsPreserveOutputGeometryOrder(t *testing.T) {
	var wire bytes.Buffer
	before, after := "before\x00\xff", "\x1b[18;60HX"
	if _, err := WriteTerminalOutput(&wire, []byte(before)); err != nil {
		t.Fatal(err)
	}
	if err := WriteTerminalGeometry(&wire, 60, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteTerminalOutput(&wire, []byte(after)); err != nil {
		t.Fatal(err)
	}
	r := TerminalReader{Reader: &wire}
	var output bytes.Buffer
	var events []string
	buf := make([]byte, 3)
	for {
		n, size, err := r.Read(buf)
		output.Write(buf[:n])
		if size != [2]uint{} {
			if n != 0 || size != [2]uint{60, 18} {
				t.Fatalf("geometry event: size=%v output=%q", size, buf[:n])
			}
			events = append(events, output.String())
			output.Reset()
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	events = append(events, output.String())
	if !slices.Equal(events, []string{before, after}) {
		t.Fatalf("output around geometry = %q", events)
	}
}
func TestTerminalControlRecordRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	want := DashAttachControl{
		Type: DashAttachControlFrame, RequestID: 9, OK: true,
		HasControl: true, ControlSessionID: "tab-1", ControlGeneration: 4,
	}
	if err := WriteTerminalControl(&wire, want); err != nil {
		t.Fatal(err)
	}
	reader := TerminalReader{Reader: &wire}
	n, geometry, err := reader.Read(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || geometry != [2]uint{} || reader.Control == nil || *reader.Control != want {
		t.Fatalf("control record = n=%d geometry=%v control=%+v, want %+v", n, geometry, reader.Control, want)
	}
}

func TestMarshalTerminalControlExplicitAuthority(t *testing.T) {
	position := TerminalPosition{Epoch: "pty-max", Sequence: TerminalSequence(^uint64(0))}
	payload, err := MarshalTerminalControl(DashAttachControl{
		Type: DashAttachControlFrame, RequestID: 3, ControlGeneration: 7, Position: position,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"has_control":false`)) {
		t.Fatalf("outbound control = %s, want explicit false authority", payload)
	}
	if !bytes.Contains(payload, []byte(`"cursor":"18446744073709551615"`)) {
		t.Fatalf("outbound control = %s, want lossless quoted cursor", payload)
	}
	var decoded DashAttachControl
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.HasControl || decoded.HighWater() != position {
		t.Fatalf("decoded control = %+v, want false authority and position %+v", decoded, position)
	}
}

func TestTerminalRecordsRejectTruncatedFrames(t *testing.T) {
	for name, data := range map[string][]byte{
		"output length": {'o'},
		"geometry":      {'g', 0, 0, 0, 80},
		"output body":   {'o', 0, 0, 0, 3, 'x'},
	} {
		t.Run(name, func(t *testing.T) {
			r := TerminalReader{Reader: bytes.NewReader(data)}
			_, _, err := r.Read(make([]byte, 32))
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated record error = %v, want unexpected EOF", err)
			}
		})
	}
}

func TestTakeoverStateAndLegacyForceRoundTrip(t *testing.T) {
	for _, want := range []DashAttachControl{
		{Type: DashAttachControlFrame, RequestID: 1, Write: true, Takeover: true},
		{Type: DashAttachTakeover, RequestID: 2, Action: "start", TakeoverID: "3c108774-c2e6-40e0-af51-2825ca226135"},
		{Type: DashAttachTakeover, OK: true, TakeoverState: &TakeoverState{
			ID: "3c108774-c2e6-40e0-af51-2825ca226135", Phase: "review",
			RequesterMemberID: "member", RequesterSessionID: "requester",
			HolderSessionID: "holder", HolderGeneration: 9,
			HoldStartedAt: "2026-10-03T00:00:00Z", HoldDeadline: "2026-10-03T00:00:05Z",
			DecisionDeadline: "2026-10-03T00:00:12Z", ServerNow: "2026-10-03T00:00:05Z",
		}},
	} {
		payload, err := MarshalTerminalControl(want)
		if err != nil {
			t.Fatal(err)
		}
		var got DashAttachControl
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		if got.Type != want.Type || got.Takeover != want.Takeover || got.Action != want.Action || got.TakeoverID != want.TakeoverID {
			t.Fatalf("decoded record = %+v, want %+v", got, want)
		}
		if want.TakeoverState != nil && (got.TakeoverState == nil || *got.TakeoverState != *want.TakeoverState) {
			t.Fatalf("decoded state = %+v, want %+v", got.TakeoverState, want.TakeoverState)
		}
		if want.Type == DashAttachTakeover && !bytes.Contains(payload, []byte(`"ok":`)) {
			t.Fatalf("takeover omitted explicit result: %s", payload)
		}
	}
}
