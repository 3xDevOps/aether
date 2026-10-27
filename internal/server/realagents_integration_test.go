//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	realAgentKey     = "aether-mock-model-api-key"
	realAgentCommand = "echo aether-$((40+2))"
	realAgentOutput  = "aether-42"
	realAgentAnswer  = "aether-turn-done"
	// realAgentContract starts the reporting rules every launch route
	// carries.
	realAgentContract = "END EVERY TURN WITH EXACTLY ONE REPORT"
	// codexCompactLimit is the token count past which the test's Codex
	// compacts; a compaction step claims more than any model's context.
	codexCompactLimit = 100000
	compactingUsage   = 1500000
)

var realAgentModes = map[domain.LaunchMode]string{domain.LaunchTUI: "standard", domain.LaunchHeadless: "background", domain.LaunchACP: "enhanced"}

func TestIntegrationRealAgents(t *testing.T) {
	image := os.Getenv("AETHER_AGENTS_IMAGE")
	if image == "" {
		t.Skip("AETHER_AGENTS_IMAGE unset; build the image with make agents-image, then run make test-agents (docs/testing.md)")
	}
	if !dockerReachable(t) {
		t.Fatal("AETHER_AGENTS_IMAGE is set but the Docker daemon is unreachable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// dockerRuntime's containers have no network, and these must reach the
	// mock model.
	docker, err := runtime.NewDocker(runtime.WithLabels(map[string]string{"aether.test": t.Name()}))
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	cli := newDockerCLI(t)
	label := "aether.test=" + t.Name()
	removeLabeledContainers(t, cli, label)
	t.Cleanup(func() { removeLabeledContainers(t, cli, label) })

	t.Setenv("AETHER_FAKE_AGENT", "sleep 600")
	e := &coordEnv{
		rt:           docker,
		image:        image,
		serverBinary: buildServerBinary(t),
		dataDir:      filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	ctrl, sshClient := srv.control(t, e.ada.key)
	sender := e.launch(t, ctrl, "send mail to the agents under test", "fake")
	senderSocket := waitMissionSocket(t, e.coordDir(sender.ID))

	// The sender's launch created the agent network, whose gateway is the
	// host as run containers reach it.
	network, err := cli.NetworkInspect(ctx, runtime.AgentNetwork, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", runtime.AgentNetwork, err)
	}
	if len(network.Network.IPAM.Config) == 0 {
		t.Fatalf("%s has no IPAM config", runtime.AgentNetwork)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(network.Network.IPAM.Config[0].Gateway.String(), "0"))
	if err != nil {
		t.Fatalf("listen for the mock model: %v", err)
	}
	model := &mockModel{answer: realAgentAnswer}
	modelServer := &httptest.Server{Listener: ln, Config: &http.Server{Handler: model}}
	modelServer.Start()
	t.Cleanup(modelServer.Close)
	t.Setenv("ANTHROPIC_API_KEY", realAgentKey)
	t.Setenv("ANTHROPIC_BASE_URL", modelServer.URL)

	claudeState, err := json.Marshal(map[string]any{
		"hasCompletedOnboarding": true,
		"customApiKeyResponses":  map[string][]string{"approved": {realAgentKey[len(realAgentKey)-20:]}},
		"projects":               map[string]any{domain.PrimaryRepoPath: map[string]bool{"hasTrustDialogAccepted": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".claude.json":          string(claudeState),
		".claude/settings.json": `{"skipDangerousModePermissionPrompt": true}`,
		".codex/auth.json":      `{"auth_mode": "apikey", "OPENAI_API_KEY": "` + realAgentKey + `"}`,
		".codex/config.toml": fmt.Sprintf("openai_base_url = %q\nmodel = \"mock-model\"\ncheck_for_update_on_startup = false\nmodel_auto_compact_token_limit = %d\n\n[projects.%q]\ntrust_level = \"trusted\"\n",
			modelServer.URL+"/v1", codexCompactLimit, domain.PrimaryRepoPath),
	} {
		path := filepath.Join(e.dataDir, "homes", string(e.ada.id), name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	launch := func(t *testing.T, a *realAgentRun, task string) {
		t.Helper()
		var launched protocol.RunLaunchResult
		if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			WorkspaceID: string(e.ws.ID), Task: task, Harness: a.agent, Mode: string(a.mode),
		}, &launched); err != nil {
			t.Fatalf("run.launch %s %s: %v", a.agent, a.mode, err)
		}
		a.run = launched.Run
		if a.mode == domain.LaunchTUI {
			a.observer = openMissionObserver(t, sshClient, a.run.ID)
		}
	}

	t.Run("agents", func(t *testing.T) {
		for _, agent := range []string{"claude", "codex"} {
			for mode, modeName := range realAgentModes {
				if mode == domain.LaunchHeadless {
					continue
				}
				t.Run(agent+"-"+modeName, func(t *testing.T) {
					t.Parallel()
					a := &realAgentRun{srv: srv, ctrl: ctrl, model: model, workspace: string(e.ws.ID), agent: agent, mode: mode, marker: "real-agent-" + agent + "-" + modeName}
					// Aether asks a turn that ended without a report for one: a
					// Standard Claude Code stop is held, an Enhanced session is
					// prompted. Standard Codex is not asked: Aether loads no
					// hooks into it, so its silent turn parks as waiting.
					gated := agent != "codex" || mode != domain.LaunchTUI
					gate := a.marker + "/gate"
					model.script(gate, step{command: realAgentCommand})
					launch(t, a, tagged(gate)+" run the command, then stop.")

					if !t.Run("turn", func(t *testing.T) {
						a.waitFor(ctx, t, "the tool call's output", func() bool { return model.saw(tagged(gate), realAgentOutput) })
						a.instructed(t)
						a.waitIdle(ctx, t, 0)
						a.waitFor(ctx, t, "the model's answer in the run", func() bool { return strings.Contains(a.transcript(), realAgentAnswer) })
						want := turnWant{activity: domain.ActivityAnswered, cause: events.CauseReport, outcome: protocol.CoordOutcomeAnswered, summary: remindedPrefix + gate, reminders: 1}
						if !gated {
							want = turnWant{activity: domain.ActivityWaiting, cause: events.CauseTurnEnd}
						}
						a.check(ctx, t, gate, 0, want)
					}) {
						return
					}

					t.Run("wake", func(t *testing.T) {
						if err := harness.SwarmRefusal(agent, mode); err != nil {
							t.Skipf("Aether cannot wake this run: %v", err)
						}
						seq := a.get(t).StatusSeq
						if err := pacedCall(ctx, senderSocket, protocol.MethodCoordSend, protocol.CoordSendParams{
							ToRunID: a.run.ID, Body: "this mail only has to wake you", IdempotencyKey: "wake-" + a.marker,
						}, nil); err != nil {
							t.Fatalf("coord.send to %s: %v", a.run.ID, err)
						}
						prompt, _, _ := strings.Cut(protocol.CoordInboxContext(1), ".")
						a.waitFor(ctx, t, "a turn prompted by the mail", func() bool { return model.saw(a.marker, prompt) })
						a.waitIdle(ctx, t, seq)
					})

					// Every turn below reports, or is reminded to, so a failure
					// says which link broke.
					turns := []struct {
						name  string
						steps func(turn string) []step
						want  func(turn string) turnWant
					}{{
						name:  "waiting",
						steps: func(turn string) []step { return []step{{command: reportCommand("waiting --for ci", turn)}} },
						want: func(turn string) turnWant {
							return turnWant{activity: domain.ActivityWaitingCI, cause: events.CauseReport, outcome: protocol.CoordOutcomeWaiting, waitingFor: protocol.CoordWaitingForCI, summary: turn}
						},
					}, {
						name:  "answered",
						steps: func(turn string) []step { return []step{{command: reportCommand("answered", turn)}} },
						want: func(turn string) turnWant {
							return turnWant{activity: domain.ActivityAnswered, cause: events.CauseReport, outcome: protocol.CoordOutcomeAnswered, summary: turn}
						},
					}, {
						// The report lands after the turn ended, which a
						// reminder may race.
						name: "late-report",
						steps: func(turn string) []step {
							// A reminder gets the same report, so whichever lands
							// last leaves the run where the late one should.
							model.remind(turn, "waiting --for process")
							// Codex ends a command's process group with it.
							return []step{{command: "setsid sh -c \"sleep 5; " + reportCommand("waiting --for process", turn) + "\" >/dev/null 2>&1 &"}}
						},
						want: func(turn string) turnWant {
							return turnWant{activity: domain.ActivityWaitingTools, cause: events.CauseReport, outcome: protocol.CoordOutcomeWaiting, waitingFor: protocol.CoordWaitingForProcess, summary: turn, reminders: -1, late: true}
						},
					}, {
						name: "compaction",
						steps: func(turn string) []step {
							model.script(compacted(turn), step{command: reportCommand("answered", turn)})
							return []step{{command: realAgentCommand, usage: compactingUsage}}
						},
						want: func(turn string) turnWant {
							// Claude Code may compact only after a held stop, so
							// the reminder can come first.
							return turnWant{activity: domain.ActivityAnswered, cause: events.CauseReport, outcome: protocol.CoordOutcomeAnswered, summary: turn, reminders: -1, compacted: true}
						},
					}, {
						name: "subagent",
						steps: func(turn string) []step {
							model.script(turn+"/sub", step{command: "sleep 8"})
							return []step{{subagent: subagentTagged(turn+"/sub") + " run the subagent's step."}}
						},
						want: func(turn string) turnWant {
							if !gated {
								return turnWant{activity: domain.ActivityWaiting, cause: events.CauseTurnEnd, subagent: turn + "/sub"}
							}
							return turnWant{activity: domain.ActivityAnswered, cause: events.CauseReport, outcome: protocol.CoordOutcomeAnswered, summary: remindedPrefix + turn, reminders: 1, subagent: turn + "/sub"}
						},
					}, {
						name:  "needs-input",
						steps: func(turn string) []step { return []step{{command: reportCommand("needs-input", turn)}} },
						want: func(turn string) turnWant {
							return turnWant{activity: domain.ActivityNeedsYou, cause: events.CauseHuman, outcome: protocol.CoordOutcomeNeedsInput, summary: turn, needsYou: true}
						},
					}}
					for i, tc := range turns {
						if !t.Run(tc.name, func(t *testing.T) {
							turn := a.marker + "/" + tc.name
							model.script(turn, tc.steps(turn)...)
							want := tc.want(turn)
							seq := a.get(t).StatusSeq
							if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
								RunID: a.run.ID, Message: tagged(turn) + " do the scripted step.", IdempotencyKey: fmt.Sprintf("%s-%d", a.marker, i),
							}, nil); err != nil {
								t.Fatalf("run.inject: %v", err)
							}
							if want.subagent != "" {
								// Its answer is its last request. Standard Claude Code
								// runs it in the background, so the main turn can end
								// waiting on it.
								var gaps, wrong []string
								a.waitFor(ctx, t, "the subagent's answer", func() bool {
									started := len(a.subagentBodies(want.subagent))
									if started == 0 {
										return false
									}
									r := a.get(t)
									state := fmt.Sprintf("%s/%s (%s: %q)", r.Status, r.Activity, r.Cause, r.Reason)
									held := domain.RunActivity(r.Activity) == domain.ActivityWaitingAgents && events.RunStatusCause(r.Cause) == events.CauseTasksHeld
									switch {
									case needsYou(r):
										wrong = append(wrong, state)
									case r.Status == string(domain.RunRunning) && (domain.RunActivity(r.Activity) == domain.ActivityWorking || held):
									default:
										gaps = append(gaps, state)
									}
									return started >= 2
								})
								if len(gaps) > 0 {
									t.Logf("known gap: Aether misses this agent's subagent, so the run showed %s while it worked", strings.Join(slices.Compact(gaps), ", "))
								}
								if len(wrong) > 0 {
									t.Errorf("while the subagent ran the run needed you: %s", strings.Join(slices.Compact(wrong), ", "))
								}
							}
							a.check(ctx, t, turn, seq, want)
						}) {
							return
						}
					}
				})
			}
		}

		// A Background run ends with its turn: it is never reminded, cannot
		// ask its user, and finishes on its report.
		for _, agent := range []string{"claude", "codex"} {
			t.Run(agent+"-background", func(t *testing.T) {
				t.Parallel()
				for _, tc := range []struct {
					name    string
					steps   func(turn string) []step
					outcome string
				}{{
					name:    "success",
					steps:   func(turn string) []step { return []step{{command: reportCommand("success", turn)}} },
					outcome: protocol.CoordOutcomeSuccess,
				}, {
					name: "silent",
					steps: func(turn string) []step {
						return []step{{command: realAgentCommand}, {command: reportCommand("needs-input", turn)}}
					},
				}} {
					t.Run(tc.name, func(t *testing.T) {
						t.Parallel()
						a := &realAgentRun{srv: srv, ctrl: ctrl, model: model, workspace: string(e.ws.ID), agent: agent, mode: domain.LaunchHeadless, marker: "real-agent-" + agent + "-background-" + tc.name}
						turn := a.marker + "/turn"
						model.script(turn, tc.steps(turn)...)
						launch(t, a, tagged(turn)+" do the scripted steps.")
						a.waitFor(ctx, t, "the run to finish", func() bool { return domain.RunStatus(a.get(t).Status).Terminal() })
						a.instructed(t)
						r := a.get(t)
						if got := domain.RunStatus(r.Status); got != domain.RunCompleted {
							t.Errorf("finished %s (%q), want %s", got, r.Reason, domain.RunCompleted)
						}
						if n := a.reminders(turn); n != 0 {
							t.Errorf("a Background run was reminded %d times", n)
						}
						receipts := a.receipts(t, turn)
						switch {
						case tc.outcome == "" && len(receipts) != 0:
							t.Errorf("unexpected report receipts %+v", receipts)
						case tc.outcome != "" && (len(receipts) != 1 || receipts[0].Outcome != tc.outcome):
							t.Errorf("report receipts %+v, want one %s", receipts, tc.outcome)
						}
						if tc.outcome == "" && !model.saw(tagged(turn), coord.ErrCannotAsk.Error()) {
							t.Errorf("the agent was not told it cannot ask:%s", model.tails(tagged(turn)))
						}
						// Only a success awaiting review needs a member.
						if got, want := needsYou(r), tc.outcome == protocol.CoordOutcomeSuccess; got != want {
							t.Errorf("needs you = %v (review %q), want %v", got, r.Review, want)
						}
					})
				}
			})
		}
	})
}

func reportCommand(outcome, turn string) string {
	return coordtransport.CLIPath + " report --outcome " + outcome + " --summary '" + turn + "'"
}

// turnWant is where a turn must leave the run. Reminders is how many
// report reminders the turn's conversation may hold, -1 for at most one.
type turnWant struct {
	activity   domain.RunActivity
	cause      events.RunStatusCause
	outcome    string
	waitingFor string
	summary    string
	needsYou   bool
	reminders  int
	compacted  bool
	subagent   string
	// late is set when the turn's report lands after the turn ended.
	late bool
}

type realAgentRun struct {
	srv       *coordServer
	ctrl      *protocol.Client
	model     *mockModel
	workspace string
	agent     string
	mode      domain.LaunchMode
	marker    string

	run      protocol.Run
	observer *attachConn
}

func (a *realAgentRun) get(t *testing.T) protocol.Run {
	t.Helper()
	var res protocol.RunResult
	if err := a.ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: a.run.ID}, &res); err != nil {
		t.Fatalf("run.get %s: %v", a.run.ID, err)
	}
	return res.Run
}

// needsYou is the rule the dashboard's Needs you list applies.
func needsYou(r protocol.Run) bool {
	run := domain.Run{
		Status: domain.RunStatus(r.Status), Activity: domain.RunActivity(r.Activity), Reason: r.Reason,
		OutcomeUnseen: r.OutcomeUnseen, MissionRole: r.MissionRole, Review: domain.RunReview(r.Review),
	}
	return run.NeedsAttention(r.Paused)
}

// instructed fails unless the launch recorded where the run instructions
// went and the agent's first request carries the reporting rules.
func (a *realAgentRun) instructed(t *testing.T) {
	t.Helper()
	noted := false
	a.events(t, func(_ protocol.Event, p events.TimelinePayload) {
		noted = noted || (p.Kind == events.TimelineNote && strings.HasPrefix(p.Message, "run instructions: "))
	})
	if !noted {
		t.Errorf("instruction missing: no run instructions note on the timeline")
	}
	bodies := a.model.bodies(a.marker)
	if len(bodies) == 0 || !strings.Contains(bodies[0], realAgentContract) {
		t.Errorf("instruction missing: the first request does not carry %q", realAgentContract)
	}
}

// check waits for turn to leave the run as want says, then names the
// broken link when it does not: the reporting rules missing from the
// requests, a report never made, a report made but lost, or a report
// recorded but misclassified.
func (a *realAgentRun) check(ctx context.Context, t *testing.T, turn string, after int64, want turnWant) {
	t.Helper()
	// A reminder's summary names its turn, so only a turn that may be
	// reminded may settle on it.
	reason := func(r protocol.Run) bool {
		return want.summary == "" || strings.Contains(r.Reason, want.summary) &&
			(strings.HasPrefix(want.summary, remindedPrefix) || want.reminders < 0 || !strings.Contains(r.Reason, remindedPrefix))
	}
	settled := func(r protocol.Run) bool {
		return r.StatusSeq > after && r.Status == string(domain.RunNeedsAttention) && domain.RunActivity(r.Activity) == want.activity &&
			events.RunStatusCause(r.Cause) == want.cause && reason(r) && (!want.late || len(a.receipts(t, want.summary)) > 0)
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	r := a.get(t)
	for !settled(r) && deadline.Err() == nil {
		select {
		case <-deadline.Done():
		case <-time.After(250 * time.Millisecond):
		}
		r = a.get(t)
	}
	receipts := a.receipts(t, want.summary)
	bodies := a.turnBodies(turn)
	var problems []string
	switch {
	case len(bodies) == 0:
		problems = append(problems, "the model never saw the turn")
	case !strings.Contains(bodies[len(bodies)-1], realAgentContract):
		problems = append(problems, "instruction missing: the turn's last request does not carry the reporting rules")
	}
	if want.compacted {
		compactedBodies := a.model.bodies(tagged(compacted(turn)))
		switch {
		case len(compactedBodies) == 0:
			problems = append(problems, "the agent never compacted")
		case !strings.Contains(compactedBodies[0], realAgentContract):
			problems = append(problems, "instruction missing: the first request after compaction does not carry the reporting rules")
		}
	}
	if want.subagent != "" {
		// The subagent answered before check, so a reminder held for its
		// stop would show.
		for _, body := range a.subagentBodies(want.subagent) {
			if strings.Contains(body, coordhooks.ReportReminderLead) {
				problems = append(problems, "the subagent was reminded to report")
				break
			}
		}
	}
	switch n := a.reminders(turn); {
	case want.reminders < 0 && n > 1:
		problems = append(problems, fmt.Sprintf("the turn was reminded %d times, want at most 1", n))
	case want.reminders >= 0 && n != want.reminders:
		problems = append(problems, fmt.Sprintf("the turn was reminded %d times, want %d", n, want.reminders))
	}
	if want.outcome != "" {
		// The report's output, as a tool output inside a request. A late
		// report's output goes nowhere.
		accepted := want.late || a.model.saw(a.marker, `\"summary\":\"`+want.summary+`\"`)
		switch {
		case len(receipts) == 0 && !accepted:
			problems = append(problems, "report never made: no report output reached the model")
		case len(receipts) == 0:
			problems = append(problems, "report lost: no timeline receipt for the report")
		case !accepted:
			problems = append(problems, "report lost: the timeline has the report but its output never reached the model")
		case len(receipts) != 1:
			problems = append(problems, fmt.Sprintf("%d report receipts, want 1", len(receipts)))
		case receipts[0].Outcome != want.outcome || receipts[0].WaitingFor != want.waitingFor:
			problems = append(problems, fmt.Sprintf("report recorded as %s/%s, want %s/%s", receipts[0].Outcome, receipts[0].WaitingFor, want.outcome, want.waitingFor))
		case want.late && !a.parkedBefore(t, after, want.summary):
			problems = append(problems, "the late report landed before the turn ended")
		}
	}
	if !settled(r) {
		problems = append(problems, fmt.Sprintf("misclassified: the run is %s/%s (cause %q, reason %q), want needs-attention/%s (cause %q, reason with %q)",
			r.Status, r.Activity, r.Cause, r.Reason, want.activity, want.cause, want.summary))
	}
	if got := needsYou(r); got != want.needsYou {
		problems = append(problems, fmt.Sprintf("Needs you = %v, want %v", got, want.needsYou))
	}
	if len(problems) > 0 {
		shown := a.transcript()
		t.Fatalf("%s %s %s:\n  %s\nreceipts: %+v\nmodel requests:%s\nshown:\n%s", a.agent, realAgentModes[a.mode], turn,
			strings.Join(problems, "\n  "), receipts, a.model.tails(a.marker), shown[max(0, len(shown)-4000):])
	}
}

// turnBodies are the requests that play turn: its tag is their last.
func (a *realAgentRun) turnBodies(turn string) []string {
	var out []string
	for _, body := range a.model.bodies(tagged(turn)) {
		body = teamMemory.ReplaceAllString(body, "")
		if strings.LastIndex(body, scriptTag) == strings.LastIndex(body, tagged(turn)) {
			out = append(out, body)
		}
	}
	return out
}

// subagentBodies are the requests of the subagent playing script.
func (a *realAgentRun) subagentBodies(script string) []string {
	var out []string
	for _, body := range a.model.bodies(subagentTagged(script)) {
		if !strings.Contains(body, scriptTag) {
			out = append(out, body)
		}
	}
	return out
}

// reminders is how many report reminders the mock answered in turn,
// before or after compaction.
func (a *realAgentRun) reminders(turn string) int {
	n := 0
	for _, script := range []string{turn, compacted(turn)} {
		most := 0
		for _, body := range a.model.bodies(remindedMark(script)) {
			most = max(most, strings.Count(body, remindedMark(script)))
		}
		n += most
	}
	return n
}

// receipts are the run's report timeline entries with summary.
func (a *realAgentRun) receipts(t *testing.T, summary string) []events.TimelinePayload {
	t.Helper()
	var out []events.TimelinePayload
	a.events(t, func(_ protocol.Event, p events.TimelinePayload) {
		if p.Kind == events.TimelineReport && p.Summary == summary {
			out = append(out, p)
		}
	})
	return out
}

// parkedBefore reports whether the run parked after status seq after and
// before its report with summary reached the timeline.
func (a *realAgentRun) parkedBefore(t *testing.T, after int64, summary string) bool {
	t.Helper()
	parked := false
	landed := false
	a.events(t, func(ev protocol.Event, p events.TimelinePayload) {
		if ev.Type == string(events.TypeRunStatus) {
			var status events.RunStatusPayload
			if err := json.Unmarshal(ev.Payload, &status); err != nil {
				t.Fatal(err)
			}
			parked = parked || (!landed && status.StatusSeq > after && status.To == domain.RunNeedsAttention)
		}
		landed = landed || (p.Kind == events.TimelineReport && p.Summary == summary)
	})
	return parked && landed
}

// events visits the run's timeline and status events, oldest first; p is
// zero for a status event.
func (a *realAgentRun) events(t *testing.T, visit func(ev protocol.Event, p events.TimelinePayload)) {
	t.Helper()
	var after uint64
	for {
		var page protocol.WorkspaceTimelineResult
		if err := a.ctrl.Call(protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{
			WorkspaceID: a.workspace, RunID: a.run.ID, Types: []string{string(events.TypeTimeline), string(events.TypeRunStatus)}, AfterSeq: after,
		}, &page); err != nil {
			t.Fatalf("workspace.timeline: %v", err)
		}
		for _, ev := range page.Events {
			var p events.TimelinePayload
			if ev.Type == string(events.TypeTimeline) {
				if err := json.Unmarshal(ev.Payload, &p); err != nil {
					t.Fatal(err)
				}
			}
			visit(ev, p)
		}
		if !page.More {
			return
		}
		after = page.NextSeq
	}
}

func (a *realAgentRun) waitIdle(ctx context.Context, t *testing.T, after int64) {
	t.Helper()
	a.waitFor(ctx, t, "the run to settle idle", func() bool {
		r := a.get(t)
		activity := domain.RunActivity(r.Activity)
		return r.StatusSeq > after && r.Status == string(domain.RunNeedsAttention) && (activity == domain.ActivityWaiting || activity == domain.ActivityAnswered)
	})
}

func (a *realAgentRun) waitFor(ctx context.Context, t *testing.T, what string, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for !done() {
		select {
		case <-ctx.Done():
			shown := a.transcript()
			t.Fatalf("%s %s: no %s: %v\nrun: %+v\nmodel requests:%s\nshown:\n%s", a.agent, realAgentModes[a.mode], what, ctx.Err(),
				a.get(t), a.model.tails(a.marker), shown[max(0, len(shown)-4000):])
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// A TUI moves the cursor between words instead of printing spaces, so
// whitespace goes with the control sequences.
var terminalControl = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]|\s`)

func (a *realAgentRun) transcript() string {
	switch a.mode {
	case domain.LaunchTUI:
		return terminalControl.ReplaceAllString(a.observer.output(), "")
	case domain.LaunchHeadless:
		return ""
	}
	page, err := a.srv.srv.sched.ACPHistory(domain.RunID(a.run.ID), 0, 0)
	if err != nil {
		return "ACP history: " + err.Error()
	}
	var b strings.Builder
	for _, it := range page.Items {
		fmt.Fprintf(&b, "%s %s", it.Kind, it.StopReason)
		if it.Message != nil {
			b.WriteString(" " + it.Message.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
