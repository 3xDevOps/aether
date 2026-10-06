package sshd

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func newACPEnv(t *testing.T) (*testEnv, *domain.Run) {
	t.Helper()
	e := newTestEnv(t, func(cfg *Config) { cfg.Control = control.New(control.Config{}) })
	run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "t", Harness: "codex", Mode: domain.LaunchACP, Status: domain.RunRunning}
	if err := e.store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	return e, run
}

func readFrames(t *testing.T, r *bufio.Reader, n int) []protocol.ACPFrame {
	t.Helper()
	frames := make([]protocol.ACPFrame, n)
	for i := range frames {
		line, err := protocol.ReadLine(r)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if err := json.Unmarshal(line, &frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	return frames
}

func TestACPStreamReplaysThenStreamsLive(t *testing.T) {
	e, run := newACPEnv(t)
	live := make(chan acphost.Item, 4)
	e.runs.acpStream = scheduler.ACPStream{
		Reset:  true,
		Epoch:  1,
		Seq:    2,
		Replay: []acphost.Item{{Seq: 1, Epoch: 1, Kind: acphost.KindTurnStart}, {Seq: 2, Epoch: 1, Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "t1", Output: strings.Repeat("x", 64<<10)}}},
		Items:  live,
		State:  &acphost.State{Mode: "auto"},
	}
	stream, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), AfterSeq: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if !ack.Live || ack.Seq != 2 || ack.Replay != 2 || ack.HasControl || !strings.Contains(string(ack.State), `"mode":"auto"`) {
		t.Fatalf("ack %+v", ack)
	}
	r := bufio.NewReader(stream)
	frames := readFrames(t, r, 3)
	if !frames[0].Reset || frames[0].Epoch != 1 || frames[1].Seq != 1 || frames[2].Seq != 2 || !frames[2].Truncated {
		t.Fatalf("frames %+v", frames)
	}
	if len(frames[2].Item) > protocol.ACPWireItemBytes {
		t.Fatalf("streamed item of %d bytes", len(frames[2].Item))
	}
	live <- acphost.Item{Seq: 3, Kind: acphost.KindTurnEnd, StopReason: "end_turn"}
	if f := readFrames(t, r, 1)[0]; f.Seq != 3 || !strings.Contains(string(f.Item), "end_turn") {
		t.Fatalf("live frame %+v", f)
	}
	close(live)
	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream after the session ended: %v", err)
	}
}

func TestACPStreamRefusesTerminalRuns(t *testing.T) {
	e := newTestEnv(t, nil)
	_, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(e.run.ID)})
	if err == nil || ack.Code != protocol.CodeInvalidParams {
		t.Fatalf("ACP on a terminal run: %+v, %v", ack, err)
	}
}

func TestACPStreamLease(t *testing.T) {
	e, run := newACPEnv(t)
	e.runs.acpStream = scheduler.ACPStream{Items: make(chan acphost.Item)}
	_, viewer := addMember(t, e, "Vic", domain.RoleViewer, false)
	_, ack, err := e.srv.Local(viewer.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "tab-3"})
	if err == nil || ack.Code != protocol.CodeDenied {
		t.Fatalf("writer without Steer: %+v, %v", ack, err)
	}
	writer, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "tab-1"})
	if err != nil || !ack.HasControl || ack.ControlGeneration == 0 {
		t.Fatalf("writer ack %+v, %v", ack, err)
	}
	defer func() { _ = writer.Close() }()
	generation := ack.ControlGeneration

	_, ack, err = e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "tab-2"})
	if err == nil || ack.Code != protocol.CodeConflict {
		t.Fatalf("second writer: %+v, %v", ack, err)
	}

	reader, ack, err := e.srv.Local(viewer.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID)})
	if err != nil || ack.HasControl {
		t.Fatalf("read-only viewer: %+v, %v", ack, err)
	}
	_ = reader.Close()

	if _, err := writer.Write([]byte(`{"type":"control","request_id":1,"control_generation":` + strconv.FormatUint(generation, 10) + "}\n")); err != nil {
		t.Fatal(err)
	}
	line, err := protocol.ReadLine(bufio.NewReader(writer))
	if err != nil {
		t.Fatal(err)
	}
	var released protocol.DashAttachControl
	if err := json.Unmarshal(line, &released); err != nil || !released.OK || released.HasControl {
		t.Fatalf("release reply %s (%v)", line, err)
	}
	if _, held := e.srv.cfg.Control.Status(string(run.ID)); held {
		t.Fatal("the lease is still held after release")
	}
}
