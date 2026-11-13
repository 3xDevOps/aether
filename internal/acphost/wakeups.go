package acphost

import (
	"encoding/json"
	"math"
	"time"

	"github.com/robfig/cron/v3"
)

// agentWakeups are the wakeups Claude Code's ScheduleWakeup and CronCreate
// tools set in the agent process. Their timers count only time the agent
// runs, so a session whose VM a sleep saved fires them late.
type agentWakeups struct {
	// at is the last one-shot wakeup's fire time.
	at time.Time
	// creates and deletes are the tool call IDs of recurring CronCreate and
	// of CronDelete calls: a recurring job runs until it is deleted.
	creates map[string]bool
	deletes map[string]bool
}

func newAgentWakeups() agentWakeups {
	return agentWakeups{creates: map[string]bool{}, deletes: map[string]bool{}}
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
	}
	if json.Unmarshal(c.RawInput, &in) != nil {
		return
	}
	switch c.ToolName {
	case "ScheduleWakeup":
		w.later(at.Add(time.Duration(in.DelaySeconds * float64(time.Second))))
	case "CronCreate":
		if in.Recurring == nil || *in.Recurring {
			w.creates[c.ID] = true
		} else if schedule, err := cron.ParseStandard(in.Cron); err == nil {
			w.later(schedule.Next(at))
		}
	case "CronDelete":
		w.deletes[c.ID] = true
	}
}

func (w *agentWakeups) later(at time.Time) {
	if at.After(w.at) {
		w.at = at
	}
}

// pending reports a wakeup the agent still waits for at now.
func (w *agentWakeups) pending(now time.Time) bool {
	return w.at.After(now) || len(w.creates) > len(w.deletes)
}

// scanAgentWakeups reads the wakeups the log records since its last reset.
func scanAgentWakeups(log *Log) (agentWakeups, error) {
	w := newAgentWakeups()
	for before := int64(math.MaxInt64); ; {
		items, err := log.ReadBefore(before, 512)
		if err != nil || len(items) == 0 {
			return w, err
		}
		for i := len(items) - 1; i >= 0; i-- {
			it := items[i]
			if it.Kind == KindReset {
				return w, nil
			}
			if it.Kind == KindToolCall {
				w.note(it.ToolCall, it.Time)
			}
		}
		before = items[0].Seq
	}
}
