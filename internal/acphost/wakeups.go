package acphost

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// agentWakeups are the wakeups Claude Code's ScheduleWakeup and CronCreate
// tools set in the agent process. Their timers count only time the agent
// runs, so a session whose VM a sleep saved fires them late.
type agentWakeups struct {
	// at is the last one-shot wakeup's fire time.
	at time.Time
	// crons are the recurring CronCreate calls not deleted, by tool call
	// ID, with the output that names the job: a recurring job runs until a
	// CronDelete names it.
	crons map[string]string
}

func newAgentWakeups() agentWakeups {
	return agentWakeups{crons: map[string]string{}}
}

// note records a completed tool call that set or deleted a wakeup at time at.
func (w *agentWakeups) note(c *ToolCall, at time.Time) {
	if c == nil || c.Status != "completed" {
		return
	}
	var in struct {
		DelaySeconds float64 `json:"delaySeconds"`
		Cron         string  `json:"cron"`
		Recurring    *bool   `json:"recurring"`
		ID           string  `json:"id"`
	}
	if json.Unmarshal(c.RawInput, &in) != nil {
		return
	}
	switch c.ToolName {
	case "ScheduleWakeup":
		w.later(at.Add(time.Duration(in.DelaySeconds * float64(time.Second))))
	case "CronCreate":
		if in.Recurring == nil || *in.Recurring {
			w.crons[c.ID] = string(c.RawOutput) + c.Output
		} else if schedule, err := cron.ParseStandard(in.Cron); err == nil {
			w.later(schedule.Next(at))
		}
	case "CronDelete":
		// A delete that names no job known here keeps the session awake.
		for id, output := range w.crons {
			if in.ID != "" && strings.Contains(output, in.ID) {
				delete(w.crons, id)
			}
		}
	}
}

func (w *agentWakeups) later(at time.Time) {
	if at.After(w.at) {
		w.at = at
	}
}

// pending reports a wakeup the agent still waits for at now.
func (w *agentWakeups) pending(now time.Time) bool {
	return w.at.After(now) || len(w.crons) > 0
}

// scanAgentWakeups reads the wakeups the log records since its last reset,
// replaying its tool calls oldest first, as a delete must follow its job.
func scanAgentWakeups(log *Log) (agentWakeups, error) {
	w := newAgentWakeups()
	var calls []Item
	replay := func() {
		for i := len(calls) - 1; i >= 0; i-- {
			w.note(calls[i].ToolCall, calls[i].Time)
		}
	}
	for before := int64(math.MaxInt64); ; {
		items, err := log.ReadBefore(before, 512)
		if err != nil {
			return w, err
		}
		if len(items) == 0 {
			replay()
			return w, nil
		}
		for i := len(items) - 1; i >= 0; i-- {
			it := items[i]
			if it.Kind == KindReset {
				replay()
				return w, nil
			}
			if it.Kind == KindToolCall {
				calls = append(calls, it)
			}
		}
		before = items[0].Seq
	}
}
