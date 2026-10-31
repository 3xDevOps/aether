//go:build integration

package server

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
)

func assertSoak(t *testing.T, e *soakEnv, prof soakProfile, ids map[string]string, identity map[string]soakContainer, watchers map[string]*soakWatcher) {
	t.Helper()
	logs := map[string][]soakEvent{}
	for _, name := range prof.probes {
		logs[name] = readEvents(ids[name], name)
	}

	var report bytes.Buffer
	fmt.Fprintf(&report, "\nrestart availability (ms between the last success before and the first success after the signal)\n")
	fmt.Fprintf(&report, "%-10s %8s %8s %8s %8s %8s %10s\n", "restart", "down_ms", "cli.a", "mcp.a", "cli.b", "mcp.b", "stream.a")
	for _, o := range e.outages {
		cell := func(name, kind string) string {
			if g, ok := gapAround(logs[name], kind, o.down); ok {
				return strconv.FormatInt(g.Milliseconds(), 10)
			}
			return "-"
		}
		stream := "-"
		w := watchers["a"]
		w.mu.Lock()
		var before, after time.Time
		for _, ob := range w.ticks {
			if ob.at.Before(o.down) {
				before = ob.at
			} else if after.IsZero() {
				after = ob.at
			}
		}
		w.mu.Unlock()
		if !before.IsZero() && !after.IsZero() {
			stream = strconv.FormatInt(after.Sub(before).Milliseconds(), 10)
		}
		fmt.Fprintf(&report, "%-10s %8d %8s %8s %8s %8s %10s\n", o.name, o.up.Sub(o.down).Milliseconds(),
			cell("a", "cli"), cell("a", "mcp"), cell("b", "cli"), cell("b", "mcp"), stream)
	}

	if w := watchers["a"]; w != nil && prof.external && prof.mail {
		emit := map[int]time.Time{}
		for _, ev := range readEvents(ids["a"], "ticker") {
			if n, err := strconv.Atoi(ev.ID); err == nil && ev.Kind == "tick" {
				emit[n] = time.UnixMilli(ev.T)
			}
		}
		w.mu.Lock()
		fmt.Fprintf(&report, "agent-side stream delay (server receipt minus the agent writing the line), max per window around each restart:\n")
		for _, o := range e.outages {
			worst, at := time.Duration(0), 0
			for n, em := range emit {
				arr, ok := w.arrivals[n]
				if !ok || em.Before(o.down.Add(-time.Second)) || em.After(o.up.Add(5*time.Second)) {
					continue
				}
				if d := arr.Sub(em); d > worst {
					worst, at = d, n
				}
			}
			fmt.Fprintf(&report, "  %-10s max %5d ms at tick %d\n", o.name, worst.Milliseconds(), at)
		}
		var steady time.Duration
		for n, em := range emit {
			if arr, ok := w.arrivals[n]; ok && arr.Sub(em) > steady {
				steady = arr.Sub(em)
			}
		}
		var never []int
		for n := range emit {
			if _, ok := w.arrivals[n]; !ok {
				never = append(never, n)
			}
		}
		sort.Ints(never)
		fmt.Fprintf(&report, "  worst of all %d lines %d ms; lines the live stream never showed: %v\n", len(emit), steady.Milliseconds(), never)
		w.mu.Unlock()
	}

	failures := map[string]int{}
	for name, evs := range logs {
		for _, ev := range evs {
			if !ev.OK {
				failures[name+"."+ev.Kind]++
			}
		}
	}
	keys := make([]string, 0, len(failures))
	for k := range failures {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&report, "agent-visible failed calls:")
	total := 0
	for _, k := range keys {
		fmt.Fprintf(&report, " %s=%d", k, failures[k])
		total += failures[k]
	}
	if total == 0 {
		fmt.Fprintf(&report, " none")
	}
	fmt.Fprintln(&report)
	if total > 0 {
		for name, evs := range logs {
			shown := 0
			for _, ev := range evs {
				if !ev.OK && shown < 3 {
					fmt.Fprintf(&report, "  first failures %s.%s at +%dms: %s\n", name, ev.Kind, ev.T-e.outages[0].down.UnixMilli(), firstLine(ev.Err))
					shown++
				}
			}
		}
		t.Errorf("%d agent-visible call failures", total)
	}

	for _, name := range prof.probes {
		if !prof.mail {
			break
		}
		other := map[string]string{"a": "b", "b": "a"}[name]
		recv := map[string]int{}
		for _, ev := range logs[other] {
			if ev.Kind == "recv" {
				recv[ev.ID]++
			}
		}
		lost, dup, sent, unsaid := 0, 0, 0, 0
		for _, ev := range logs[name] {
			if ev.Kind != "send" {
				continue
			}
			n := recv[ev.ID]
			switch {
			case ev.OK:
				sent++
				if n == 0 {
					lost++
				}
			case n > 0 && !strings.Contains(ev.Err, "may already have applied"):
				unsaid++
				t.Errorf("send %s failed with %q but was delivered: the error must say it may already have applied", ev.ID, firstLine(ev.Err))
			}
			if n > 1 {
				dup++
			}
		}
		fmt.Fprintf(&report, "mail %s->%s: acknowledged sends=%d lost=%d duplicated=%d misreported-failures=%d\n", name, other, sent, lost, dup, unsaid)
		if lost > 0 || dup > 0 {
			t.Errorf("mail %s->%s: %d lost, %d duplicated of %d sends", name, other, lost, dup, sent)
		}
	}

	if prof.burst {
		for _, name := range prof.probes {
			termLog, _ := containerFile(ids[name], "/workspace/soak/term.log")
			for _, ev := range logs[name] {
				if ev.Kind != "term-input" {
					continue
				}
				applied := strings.Count(termLog, ev.ID)
				fmt.Fprintf(&report, "terminal input %s: ok=%v applied=%d %s\n", ev.ID, ev.OK, applied, firstLine(ev.Err))
				if ev.OK {
					found := false
					for _, out := range logs[name] {
						if out.Kind == "term-output" && out.ID == ev.ID {
							found = true
							if !out.OK {
								t.Errorf("terminal output after input %s: %s", ev.ID, out.Err)
							}
						}
					}
					if !found {
						t.Errorf("terminal input %s has no output check", ev.ID)
					}
				}
				if ev.OK && applied != 1 {
					t.Errorf("terminal input %s was accepted but applied %d times, want once", ev.ID, applied)
				}
				if applied > 1 {
					t.Errorf("unkeyed terminal input %s was applied %d times", ev.ID, applied)
				}
				if !ev.OK && applied > 0 && !strings.Contains(ev.Err, "may already have applied") {
					t.Errorf("terminal input %s failed with %q but was applied", ev.ID, firstLine(ev.Err))
				}
			}
		}
	}
	for _, name := range prof.probes {
		id := ids[name]
		marker, err := containerFile(id, "/workspace/soak/marker."+name)
		want := make([]string, prof.long)
		for i := range want {
			want[i] = strconv.Itoa(i + 1)
		}
		got := strings.Fields(marker)
		if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("run %s: the long command's marker has %d lines, want %d contiguous (err %v)", name, len(got), prof.long, err)
		}
		now := containerIdentity(t, id)
		if now != identity[id] {
			t.Errorf("run %s: the container changed: %+v -> %+v", name, identity[id], now)
		}
		fmt.Fprintf(&report, "long command %s: %d/%d lines, container %.12s started %s\n", name, len(got), prof.long, now.id, now.started)
	}

	for _, name := range append(append([]string{}, prof.probes...), "c") {
		if name == "b" && prof.external && prof.mail {
			continue
		}
		id := ids[name]
		items, err := e.history(id)
		if err != nil {
			t.Errorf("run %s: read the log: %v", name, err)
			continue
		}
		var interrupted []string
		var text strings.Builder
		for _, it := range items {
			if it.Kind == acphost.KindNotice && it.Notice != nil {
				fmt.Fprintf(&report, "run %s notice: %s: %s\n", name, it.Notice.Title, firstLine(it.Notice.Description))
			}
			switch {
			case it.Kind == acphost.KindNotice && it.Notice != nil && strings.Contains(strings.ToLower(it.Notice.Title+it.Notice.Description), "interrupt"):
				interrupted = append(interrupted, it.Notice.Title+": "+firstLine(it.Notice.Description))
			case it.Kind == acphost.KindTurnEnd && it.StopReason != "" && it.StopReason != "end_turn":
				interrupted = append(interrupted, "turn_end:"+it.StopReason)
			case it.Kind == acphost.KindMessage && it.Message != nil && it.Message.Role == "assistant":
				text.WriteString(it.Message.Text)
				if len(it.Message.Text) > 20 {
					fmt.Fprintf(&report, "run %s coalesced message item seq=%d len=%d starts %q\n", name, it.Seq, len(it.Message.Text), it.Message.Text[:16])
				}
			}
		}
		fmt.Fprintf(&report, "run %s: interrupted-turn markers=%v\n", name, interrupted)
		if len(interrupted) > 0 {
			t.Errorf("run %s: the turn was interrupted: %v", name, interrupted)
		}
		switch name {
		case "c":
			if !strings.Contains(text.String(), "permission: allow") {
				t.Errorf("run C: the answered permission request never reached the agent; assistant said %q", text.String())
			}
		default:
			seen := map[int]int{}
			top := 0
			for _, line := range strings.Split(text.String(), "\n") {
				if v, ok := strings.CutPrefix(line, "tick "); ok {
					if n, convErr := strconv.Atoi(v); convErr == nil {
						seen[n]++
						top = max(top, n)
					}
				}
			}
			missing, dup := 0, 0
			var gaps []string
			// The item log keeps only the newest items, so lines before the
			// oldest retained one are not a loss.
			first := top
			for n := range seen {
				first = min(first, n)
			}
			for n := first; n <= top; n++ {
				switch seen[n] {
				case 0:
					missing++
					gaps = append(gaps, fmt.Sprintf("-%d", n))
				case 1:
				default:
					dup++
					gaps = append(gaps, fmt.Sprintf("+%d", n))
				}
			}
			if len(gaps) > 0 {
				fmt.Fprintf(&report, "run %s: stream anomalies (- missing, + repeated): %s\n", name, strings.Join(gaps, " "))
			}
			fmt.Fprintf(&report, "run %s: streamed ticks=%d missing=%d duplicated=%d\n", name, top, missing, dup)
			if (!prof.external || prof.mail) && (missing > 0 || dup > 0 || top == 0) {
				t.Errorf("run %s: the stream lost %d and repeated %d of %d lines", name, missing, dup, top)
			}
			w := watchers[name]
			w.mu.Lock()
			bad := map[string]bool{}
			for _, s := range w.statuses {
				if strings.Contains(s, "interrupt") || strings.HasPrefix(s, "failed") {
					bad[s] = true
				}
			}
			w.mu.Unlock()
			if len(bad) > 0 {
				t.Errorf("run %s showed %v during the restarts", name, bad)
			}
		}
	}
	t.Log(report.String())
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 700 {
		line = line[:700]
	}
	return line
}
