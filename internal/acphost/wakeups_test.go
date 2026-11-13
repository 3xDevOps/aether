package acphost

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func wakeupCall(id, tool string, input map[string]any) *ToolCall {
	raw, _ := json.Marshal(input)
	return &ToolCall{ID: id, ToolName: tool, Status: "completed", RawInput: raw}
}

func cronCall(id string, input map[string]any, output string) *ToolCall {
	c := wakeupCall(id, "CronCreate", input)
	c.Output = output
	return c
}

func TestAgentWakeupsArePendingUntilTheyFire(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	w := newAgentWakeups()
	w.note(&ToolCall{ID: "w0", ToolName: "ScheduleWakeup", Status: "in_progress", RawInput: json.RawMessage(`{"delaySeconds":1200}`)}, at)
	if w.pending(at) {
		t.Fatal("a wakeup counted before its tool call completed")
	}
	w.note(wakeupCall("w1", "ScheduleWakeup", map[string]any{"delaySeconds": 1200}), at)
	if !w.pending(at.Add(19*time.Minute)) || w.pending(at.Add(21*time.Minute)) {
		t.Fatal("ScheduleWakeup is not pending exactly until it fires")
	}
	w.note(cronCall("c1", map[string]any{"cron": "0 12 * * *", "recurring": false}, "Scheduled one-shot job job-1"), at)
	if !w.pending(at.Add(time.Hour)) || w.pending(at.Add(3*time.Hour)) {
		t.Fatal("a one-shot CronCreate is not pending exactly until noon")
	}
	w.note(cronCall("c2", map[string]any{"cron": "*/5 * * * *"}, "Scheduled recurring job job-2"), at)
	if !w.pending(at.Add(48 * time.Hour)) {
		t.Fatal("a recurring CronCreate stopped counting")
	}
	w.note(wakeupCall("d1", "CronDelete", map[string]any{"id": "job-1"}), at)
	if !w.pending(at.Add(48 * time.Hour)) {
		t.Fatal("deleting the one-shot job ended the recurring one")
	}
	w.note(wakeupCall("d2", "CronDelete", map[string]any{"id": "job-2"}), at)
	if w.pending(at.Add(48 * time.Hour)) {
		t.Fatal("a recurring CronCreate still counts after CronDelete named it")
	}
}

func TestAgentWakeupsAreReadBackFromTheLog(t *testing.T) {
	t.Parallel()
	log, err := OpenLog(filepath.Join(t.TempDir(), "items.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	at := time.Now().UTC()
	for _, it := range []Item{
		{Kind: KindToolCall, Time: at.Add(-2 * time.Hour), ToolCall: wakeupCall("old", "ScheduleWakeup", map[string]any{"delaySeconds": 60})},
		{Kind: KindReset, Time: at.Add(-time.Hour)},
		{Kind: KindToolCall, Time: at, ToolCall: cronCall("cron", map[string]any{"cron": "*/5 * * * *"}, "Scheduled job job-3")},
	} {
		if err = log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	w, err := scanAgentWakeups(log)
	if err != nil {
		t.Fatal(err)
	}
	if !w.pending(at.Add(time.Hour)) || len(w.crons) != 1 || !w.at.IsZero() {
		t.Fatalf("scanned wakeups = %+v, want the recurring cron after the reset only", w)
	}
}
