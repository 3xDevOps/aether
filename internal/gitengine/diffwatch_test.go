package gitengine

import (
	"testing"
	"time"
)

func TestDiffWatchRefreshDoesNotSpinWhenTreeIdle(t *testing.T) {
	now := time.Now()
	w := &diffWatch{
		e: &Engine{cfg: Config{
			QuietPeriod: time.Hour,
			MinInterval: time.Hour,
			MaxInterval: 2 * time.Hour,
		}},
		lastEvent:            now.Add(-4 * time.Hour),
		lastSnap:             now.Add(-4 * time.Hour),
		ignoreRefreshPending: true,
		ignoreRefreshAt:      now.Add(time.Hour),
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	w.arm(timer, now)
	select {
	case <-timer.C:
		t.Fatal("idle tree scheduled immediate work before the pending ignore refresh")
	default:
	}
}

func TestDiffWatchTreeDeadlineIsNotDelayedByHeadEvents(t *testing.T) {
	now := time.Now()
	w := &diffWatch{
		e: &Engine{cfg: Config{
			QuietPeriod: time.Hour,
			MinInterval: time.Hour,
			MaxInterval: 2 * time.Hour,
		}},
		dirty:     true,
		headDirty: true,
		lastEvent: now.Add(-4 * time.Hour),
		lastSnap:  now.Add(-4 * time.Hour),
		headEvent: now,
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	w.arm(timer, now)
	select {
	case <-timer.C:
	case <-time.After(time.Second):
		t.Fatal("future HEAD deadline delayed an overdue tree snapshot")
	}
}
