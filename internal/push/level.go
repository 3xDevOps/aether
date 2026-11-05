package push

import (
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Kinds of notification, as a level routes them and a digest counts them.
// Only a need is a blocker.
const (
	KindNeed   = "need"
	KindDone   = "done"
	KindFailed = "failed"
	KindMerged = "merged"
	KindChecks = "checks"
	KindOther  = "other"
)

// Route is what becomes of a notification, each later than the one
// before.
type Route int

const (
	// RouteNow pushes it at once.
	RouteNow Route = iota
	// RouteDigest queues it for the member's next digest.
	RouteDigest
	// RouteOvernight queues it for the digest sent when quiet hours end.
	RouteOvernight
)

// RouteOf routes a notification of kind for a member with p at now. Quiet
// hours hold everything; otherwise the level decides.
func RouteOf(p store.NotifyPrefs, kind string, now time.Time) Route {
	if _, quiet := QuietUntil(p, now); quiet {
		return RouteOvernight
	}
	if p.Level == protocol.NotifyAll || kind == KindNeed {
		return RouteNow
	}
	return RouteDigest
}

// digestDue is when items, a member's queue, oldest first, are sent: when
// quiet hours end, at once after them if they held any, else on the next
// half hour in the member's zone after the oldest at the batched level, at the next digest time
// at the blockers level, and at once at all.
func digestDue(p store.NotifyPrefs, items []*store.DigestItem, now time.Time) time.Time {
	if until, quiet := QuietUntil(p, now); quiet {
		return until
	}
	for _, item := range items {
		if item.Overnight {
			return now
		}
	}
	oldest := items[0].CreatedAt
	switch p.Level {
	case protocol.NotifyBatched:
		local := oldest.In(Zone(p.DigestTZ))
		return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), (local.Minute()/30+1)*30, 0, 0, local.Location())
	case protocol.NotifyBlockers:
		return nextClock(p.DigestAt, Zone(p.DigestTZ), oldest)
	}
	return now
}

// nextClock is the first time after t whose clock in loc reads hhmm.
func nextClock(hhmm string, loc *time.Location, t time.Time) time.Time {
	m, _ := ParseClock(hhmm)
	local := t.In(loc)
	at := time.Date(local.Year(), local.Month(), local.Day(), m/60, m%60, 0, 0, loc)
	if !at.After(t) {
		at = time.Date(local.Year(), local.Month(), local.Day()+1, m/60, m%60, 0, 0, loc)
	}
	return at
}

// QuietUntil reports whether now falls in p's quiet hours, and when they
// end.
func QuietUntil(p store.NotifyPrefs, now time.Time) (time.Time, bool) {
	if p.QuietTZ == "" {
		return time.Time{}, false
	}
	start, okStart := ParseClock(p.QuietStart)
	end, okEnd := ParseClock(p.QuietEnd)
	if !okStart || !okEnd || start == end {
		return time.Time{}, false
	}
	local := now.In(Zone(p.QuietTZ))
	m := local.Hour()*60 + local.Minute()
	quiet := start <= m && m < end
	if start > end {
		quiet = m >= start || m < end
	}
	if !quiet {
		return time.Time{}, false
	}
	until := time.Date(local.Year(), local.Month(), local.Day(), end/60, end%60, 0, 0, local.Location())
	if !until.After(local) {
		until = until.AddDate(0, 0, 1)
	}
	return until, true
}

// Zone is the IANA zone name, UTC when it is empty, unknown or Local: the
// server's own zone is not the member's.
func Zone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return time.UTC
	}
	return loc
}

// ParseClock reads HH:MM as minutes after midnight.
func ParseClock(hhmm string) (int, bool) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// needKind is the kind of a session's notification to its owner: a
// success to review is done, a failure nobody has seen failed, and the
// rest need them.
func needKind(run *domain.Run, n *need) string {
	_, key, _ := strings.Cut(n.key, "|")
	switch {
	case strings.HasPrefix(key, "finished:"):
		return KindDone
	case strings.HasPrefix(key, "status:") && !run.NeedsAttention(false):
		return KindFailed
	}
	return KindNeed
}

// NoticeKind is the kind of a watch notice with condition and body.
func NoticeKind(condition, body string) string {
	switch condition {
	case protocol.WatchNeedsYou:
		return KindNeed
	case protocol.WatchFinished:
		switch {
		case body == "Failed" || strings.HasPrefix(body, "Failed: "):
			return KindFailed
		case body == "Closed as merged":
			return KindMerged
		}
		return KindDone
	case protocol.WatchPRMerged, protocol.WatchMerged:
		return KindMerged
	case protocol.WatchCI:
		return KindChecks
	}
	return KindOther
}
