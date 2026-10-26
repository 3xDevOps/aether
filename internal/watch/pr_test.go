package watch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	ghToken = "gho_test_only_watch_credential"
	shaOne  = "1111111111111111111111111111111111111111"
	shaTwo  = "2222222222222222222222222222222222222222"
)

type fakeCheck struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Completed  string `json:"completed_at,omitempty"`
}

type fakeReview struct {
	ID        int64  `json:"id"`
	State     string `json:"state"`
	Body      string `json:"body"`
	User      string `json:"login"`
	Submitted string `json:"submitted_at"`
}

// fakeGitHub is api.github.com for one pull request, acme/app#7. Bodies
// carry content ETags and a matching If-None-Match answers 304, as GitHub
// does. limitUntil answers every request with a primary rate limit.
type fakeGitHub struct {
	t      *testing.T
	server *httptest.Server

	mu          sync.Mutex
	state       string
	mergedAt    string
	head        string
	checks      map[string][]fakeCheck
	reviews     []fakeReview
	limitUntil  time.Time
	requests    []string
	conditional int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, state: "open", head: shaOne, checks: map[string][]fakeCheck{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	target, _ := url.Parse(f.server.URL)
	base := http.DefaultTransport
	http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" {
			return base.RoundTrip(r)
		}
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return base.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = base })
	return f
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.RequestURI()
	f.requests = append(f.requests, path)
	if r.Header.Get("Authorization") != "Bearer "+ghToken {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	if time.Now().Before(f.limitUntil) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(f.limitUntil.Unix()))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		return
	}
	var body any
	switch {
	case path == "/user":
		body = map[string]any{"id": 42, "login": "octocat"}
	case path == "/repos/acme/app/pulls/7":
		var merged any
		if f.mergedAt != "" {
			merged = f.mergedAt
		}
		body = map[string]any{"number": 7, "title": "Fix login", "html_url": "https://github.com/acme/app/pull/7",
			"state": f.state, "merged_at": merged, "head": map[string]string{"sha": f.head}, "base": map[string]string{"ref": "main"}}
	case strings.HasPrefix(path, "/repos/acme/app/commits/"+f.head+"/check-runs?"):
		runs := []map[string]any{}
		for _, c := range f.checks[f.head] {
			run := map[string]any{"id": c.ID, "name": c.Name, "status": c.Status, "conclusion": nil, "html_url": "https://github.com/acme/app/runs/1",
				"check_suite": map[string]int{"id": 9}, "app": map[string]string{"name": "GitHub Actions"}}
			if c.Conclusion != "" {
				run["conclusion"], run["completed_at"] = c.Conclusion, c.Completed
			}
			runs = append(runs, run)
		}
		body = map[string]any{"total_count": len(runs), "check_runs": runs}
	case strings.HasPrefix(path, "/repos/acme/app/commits/"+f.head+"/check-suites?"):
		body = map[string]any{"total_count": 0, "check_suites": []any{}}
	case strings.HasPrefix(path, "/repos/acme/app/commits/"+f.head+"/status?"):
		body = map[string]any{"state": "success", "statuses": []any{}}
	case strings.HasPrefix(path, "/repos/acme/app/pulls/7/reviews?"):
		reviews := []map[string]any{}
		for _, rv := range f.reviews {
			reviews = append(reviews, map[string]any{"id": rv.ID, "state": rv.State, "body": rv.Body, "user": map[string]string{"login": rv.User},
				"html_url": "https://github.com/acme/app/pull/7#review", "submitted_at": rv.Submitted})
		}
		body = reviews
	default:
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	data, _ := json.Marshal(body)
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" {
		f.conditional++
		if match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = w.Write(data)
}

func (f *fakeGitHub) set(change func(f *fakeGitHub)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// reads counts requests for the pull request itself, one per snapshot,
// every request but the credential check, and the conditional ones.
func (f *fakeGitHub) reads() (pulls, all, conditional int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		switch r {
		case "/repos/acme/app/pulls/7":
			pulls++
		case "/user":
			continue
		}
		all++
	}
	return pulls, all, f.conditional
}

// prHarness is the watch harness with pull request watches read from a
// fake GitHub through the real github.Service and the owner's gh login.
type prHarness struct {
	*harness
	gh    *fakeGitHub
	homes *memberhome.Manager
}

func newPRHarness(t *testing.T) *prHarness {
	t.Helper()
	gh := newFakeGitHub(t)
	h := newHarness(t)
	_ = h.svc.Close()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &prHarness{harness: h, gh: gh, homes: homes}
	p.login(h.ada)
	p.startPR()
	return p
}

// startPR runs a new Service with a new PRs source, as a server start does.
func (p *prHarness) startPR() {
	p.t.Helper()
	svc, err := New(Config{
		Store: p.db, Bus: p.bus, Runs: p.runs, Swarm: p.swarmOf, Push: p.push, Head: p.log.LastSeq,
		Debounce: 50 * time.Millisecond, Tick: 50 * time.Millisecond, Now: p.clock.now,
	})
	if err != nil {
		p.t.Fatalf("New: %v", err)
	}
	prs, err := NewPRs(PRConfig{GitHub: github.New(p.homes), Store: p.db, Bus: p.bus, Now: p.clock.now})
	if err != nil {
		p.t.Fatalf("NewPRs: %v", err)
	}
	svc.UsePRSource(prs)
	if err = svc.Start(p.ctx); err != nil {
		p.t.Fatalf("Start: %v", err)
	}
	p.t.Cleanup(func() { _ = svc.Close() })
	p.svc = svc
}

func (p *prHarness) login(m *domain.Member) {
	p.t.Helper()
	home, err := p.homes.Path(m.ID)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "gh"), 0o700); err != nil {
		p.t.Fatal(err)
	}
	hosts := "github.com:\n  user: octocat\n  oauth_token: " + ghToken + "\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "gh", "hosts.yml"), []byte(hosts), 0o600); err != nil {
		p.t.Fatal(err)
	}
}

// poll moves past the longest poll interval, so the next pass reads
// GitHub again, and waits for it.
func (p *prHarness) poll(id string) {
	p.t.Helper()
	before := p.watchState(id).LastCheckAt
	p.clock.advance(PRPollIdle + PRPollIdle/5)
	p.eventually("a poll", func() bool {
		after := p.watchState(id).LastCheckAt
		return after != nil && (before == nil || after.After(*before))
	})
	settle()
}

func (p *prHarness) watchOn(member *domain.Member, target protocol.WatchTarget, conditions ...string) protocol.WatchResult {
	p.t.Helper()
	res, err := p.svc.Create(p.ctx, member.ID, protocol.WatchCreateParams{Target: target, Conditions: conditions})
	if err != nil {
		p.t.Fatalf("Create: %v", err)
	}
	return res
}

func (p *prHarness) stamp() string { return p.clock.now().UTC().Format(time.RFC3339) }

func prTarget7() protocol.WatchTarget {
	return protocol.WatchTarget{Kind: protocol.WatchTargetPR, Repo: "acme/app", Number: 7}
}

func TestPRChecksFailRerunPassNotifyOnceEach(t *testing.T) {
	p := newPRHarness(t)
	p.gh.set(func(f *fakeGitHub) {
		f.checks[shaOne] = []fakeCheck{{ID: 1, Name: "build", Status: "in_progress"}, {ID: 2, Name: "lint", Status: "queued"}, {ID: 3, Name: "test", Status: "queued"}}
	})
	res := p.watchOn(p.ada, prTarget7(), protocol.WatchCI, protocol.WatchReview)
	w := res.Watch
	if w.TargetTitle != "acme/app#7: Fix login" || w.State != protocol.WatchActive {
		t.Fatalf("watch = %q %s %s, want the pull request's title and active", w.TargetTitle, w.State, w.Error)
	}
	p.eventually("the first read", func() bool { return p.watchState(w.ID).LastCheckAt != nil })
	if next := p.watchState(w.ID).NextCheckAt; next == nil || next.Sub(p.clock.now()) > PRPollBusy+PRPollBusy/5 {
		t.Fatalf("next_check_at = %v, want about a minute while checks run", next)
	}

	done := p.stamp()
	p.gh.set(func(f *fakeGitHub) {
		f.checks[shaOne] = []fakeCheck{{ID: 1, Name: "build", Status: "completed", Conclusion: "failure", Completed: done},
			{ID: 2, Name: "lint", Status: "completed", Conclusion: "failure", Completed: done},
			{ID: 3, Name: "test", Status: "completed", Conclusion: "success", Completed: done}}
	})
	p.poll(w.ID)
	p.poll(w.ID)
	p.poll(w.ID)
	if got := bodies(p.notices(w.ID)); !slices.Equal(got, []string{"ci: 2 checks failed on #7: build, lint"}) {
		t.Fatalf("notices after duplicate polls = %q", got)
	}
	if _, _, conditional := p.gh.reads(); conditional == 0 {
		t.Fatal("polls never asked GitHub conditionally")
	}
	if next := p.watchState(w.ID).NextCheckAt; next == nil || next.Sub(p.clock.now()) < PRPollIdle-PRPollIdle/5 {
		t.Fatalf("next_check_at = %v, want about five minutes once nothing changes", next)
	}

	p.gh.set(func(f *fakeGitHub) {
		f.checks[shaOne] = []fakeCheck{{ID: 4, Name: "build", Status: "queued"}, {ID: 5, Name: "lint", Status: "queued"},
			{ID: 3, Name: "test", Status: "completed", Conclusion: "success", Completed: done}}
	})
	p.poll(w.ID)
	rerun := p.stamp()
	p.gh.set(func(f *fakeGitHub) {
		f.checks[shaOne] = []fakeCheck{{ID: 1, Name: "build", Status: "completed", Conclusion: "failure", Completed: done},
			{ID: 2, Name: "lint", Status: "completed", Conclusion: "failure", Completed: done},
			{ID: 4, Name: "build", Status: "completed", Conclusion: "success", Completed: rerun},
			{ID: 5, Name: "lint", Status: "completed", Conclusion: "success", Completed: rerun},
			{ID: 3, Name: "test", Status: "completed", Conclusion: "success", Completed: done}}
		f.reviews = []fakeReview{
			{ID: 10, State: "COMMENTED", User: "hubot", Submitted: rerun},
			{ID: 11, State: "CHANGES_REQUESTED", Body: "Rename it", User: "mona", Submitted: rerun},
		}
	})
	p.poll(w.ID)
	p.poll(w.ID)

	pushed := p.stamp()
	p.gh.set(func(f *fakeGitHub) {
		f.head = shaTwo
		f.checks[shaTwo] = []fakeCheck{{ID: 6, Name: "build", Status: "completed", Conclusion: "success", Completed: pushed}}
		f.reviews = append(f.reviews, fakeReview{ID: 12, State: "APPROVED", User: "mona", Submitted: pushed})
	})
	p.poll(w.ID)
	p.poll(w.ID)
	want := []string{
		"ci: 2 checks failed on #7: build, lint",
		"ci: Checks passed on #7",
		"ci: Checks passed on #7",
		"review: mona approved #7",
		"review: mona requested changes on #7",
	}
	if got := bodies(p.notices(w.ID)); !slices.Equal(slices.Sorted(slices.Values(got)), want) {
		t.Fatalf("notices = %q, want %q", got, want)
	}
	obs := p.watchState(w.ID)
	var observed protocol.WatchObserved
	_ = json.Unmarshal(obs.Observed, &observed)
	if observed.HeadSHA != shaTwo || observed.ReviewState != "approved" || observed.Checks == nil || observed.Checks.Passed != 1 {
		t.Fatalf("observed = %+v", observed)
	}
	for _, n := range p.notices(w.ID) {
		if n.Link != "https://github.com/acme/app/pull/7" || n.Title != "acme/app#7: Fix login" {
			t.Fatalf("notice %q links %q titled %q", n.Body, n.Link, n.Title)
		}
	}
}

func TestPRRestartBetweenPollsAsksConditionallyAndDoesNotRenotify(t *testing.T) {
	p := newPRHarness(t)
	w := p.watchOn(p.ada, prTarget7(), protocol.WatchCI).Watch
	done := p.stamp()
	p.gh.set(func(f *fakeGitHub) {
		f.checks[shaOne] = []fakeCheck{{ID: 1, Name: "build", Status: "completed", Conclusion: "success", Completed: done}}
	})
	p.poll(w.ID)
	if got := bodies(p.notices(w.ID)); len(got) != 1 {
		t.Fatalf("notices = %q, want one pass", got)
	}

	_ = p.svc.Close()
	p.gh.set(func(f *fakeGitHub) { f.requests, f.conditional = nil, 0 })
	p.startPR()
	p.poll(w.ID)
	if got := bodies(p.notices(w.ID)); len(got) != 1 {
		t.Fatalf("notices after restart = %q, want the pass once", got)
	}
	if _, all, conditional := p.gh.reads(); all == 0 || conditional != all {
		t.Fatalf("after restart %d of %d requests were conditional, want all: the ETags were persisted", conditional, all)
	}
}

func TestPRTwoWatchesOnOnePullRequestShareOneRead(t *testing.T) {
	p := newPRHarness(t)
	w1 := p.watchOn(p.ada, prTarget7(), protocol.WatchCI).Watch
	w2 := p.watchOn(p.ada, prTarget7(), protocol.WatchMerged).Watch
	p.eventually("both read", func() bool {
		return p.watchState(w1.ID).LastCheckAt != nil && p.watchState(w2.ID).LastCheckAt != nil
	})
	settle()
	if reads, _, _ := p.gh.reads(); reads != 1 {
		t.Fatalf("GitHub reads for two new watches = %d, want 1", reads)
	}
}

func TestPRRateLimitWaitsForTheReset(t *testing.T) {
	p := newPRHarness(t)
	w := p.watchOn(p.ada, prTarget7(), protocol.WatchCI).Watch
	p.eventually("the first read", func() bool { return p.watchState(w.ID).LastCheckAt != nil })
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	p.gh.set(func(f *fakeGitHub) { f.limitUntil = reset })
	p.clock.advance(PRPollIdle + PRPollIdle/5)
	p.eventually("the rate limit", func() bool { return p.watchState(w.ID).State == protocol.WatchError })
	got := p.watchState(w.ID)
	if want := "GitHub rate limit, retrying at " + reset.UTC().Format(time.RFC3339); !strings.HasPrefix(got.Error, want) {
		t.Fatalf("error = %q, want it to start %q", got.Error, want)
	}
	_, before, _ := p.gh.reads()
	p.clock.advance(10 * time.Minute)
	settle()
	p.clock.advance(10 * time.Minute)
	settle()
	if _, after, _ := p.gh.reads(); after != before {
		t.Fatalf("%d requests sent before the reset", after-before)
	}
	p.gh.set(func(f *fakeGitHub) { f.limitUntil = time.Time{} })
	p.clock.advance(time.Hour)
	p.eventually("recovery after the reset", func() bool { return p.watchState(w.ID).State == protocol.WatchActive })
}

func TestPRWatchWithoutGitHubLoginSaysHowToFixIt(t *testing.T) {
	p := newPRHarness(t)
	res := p.watchOn(p.bob, prTarget7(), protocol.WatchCI)
	if res.Watch.State != protocol.WatchError || !strings.Contains(res.Watch.Error, "run aether github connect") ||
		!strings.Contains(res.Watch.Error, "no native gh login found") {
		t.Fatalf("watch = %s %q, want the error state with the fix and the real error", res.Watch.State, res.Watch.Error)
	}
	p.login(p.bob)
	p.clock.advance(time.Minute)
	p.eventually("the watch to recover once logged in", func() bool { return p.watchState(res.Watch.ID).State == protocol.WatchActive })

	p.gh.set(func(f *fakeGitHub) { f.state = "closed" })
	_, err := p.svc.Create(p.ctx, p.ada.ID, protocol.WatchCreateParams{Target: prTarget7(), Conditions: []string{protocol.WatchMerged, protocol.WatchClosed}})
	if err == nil || !strings.Contains(err.Error(), "already closed on GitHub") {
		t.Fatalf("watching a closed pull request: %v, want it refused", err)
	}
}

func TestRunPRMergedOnlyFromGitHubNotFromTheAgentsReport(t *testing.T) {
	p := newPRHarness(t)
	run := p.run(p.ada, "Fix login")
	if err := p.db.RecordRunPullRequest(p.ctx, &store.RunPullRequest{RunID: run.ID, Repository: "acme/app", Number: 7,
		URL: "https://github.com/acme/app/pull/7", State: "open", ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sub, err := p.bus.Subscribe(p.ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunPR}}, Buffer: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close() //nolint:errcheck // test cleanup
	w := p.watchOn(p.ada, runTarget(run), protocol.WatchFinished, protocol.WatchPRMerged).Watch

	if _, err = p.db.FinishRunReported(p.ctx, run.ID, domain.RunCompleted, "agent reported success", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	p.publish(run.ID, events.RunStatusPayload{To: domain.RunCompleted})
	p.eventually("the reported finish", func() bool { return len(p.notices(w.ID)) == 1 })
	p.poll(w.ID)
	if got := bodies(p.notices(w.ID)); !slices.Equal(got, []string{"finished: Agent reported done"}) {
		t.Fatalf("notices with the pull request open = %q", got)
	}
	if st := p.watchState(w.ID).State; st != protocol.WatchActive {
		t.Fatalf("state = %s, want active while the pull request is open", st)
	}

	merged := p.stamp()
	p.gh.set(func(f *fakeGitHub) { f.state, f.mergedAt = "closed", merged })
	p.poll(w.ID)
	if got := bodies(p.notices(w.ID)); !slices.Equal(got, []string{"finished: Agent reported done", "pr_merged: PR #7 merged on GitHub"}) {
		t.Fatalf("notices = %q", got)
	}
	if got := p.watchState(w.ID); got.State != protocol.WatchActive || got.NextCheckAt != nil {
		t.Fatalf("state = %s, next_check_at = %v; want polling stopped, the completed run still watched until it is closed", got.State, got.NextCheckAt)
	}
	prs, err := p.db.RunPullRequests(p.ctx, run.ID)
	if err != nil || len(prs) != 1 || prs[0].State != "merged" || prs[0].MergedAt == nil {
		t.Fatalf("run pull requests = %+v, %v; want the merge recorded", prs, err)
	}
	select {
	case e := <-sub.Events():
		if pr, ok := e.Payload.(events.RunPRPayload); !ok || pr.State != "merged" {
			t.Fatalf("run.pr = %+v", e.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no run.pr event for the merge")
	}
}

func TestPRClosedWithoutMergingEndsTheWatch(t *testing.T) {
	p := newPRHarness(t)
	w := p.watchOn(p.ada, prTarget7(), protocol.WatchMerged, protocol.WatchClosed).Watch
	p.eventually("the first read", func() bool { return p.watchState(w.ID).LastCheckAt != nil })
	p.gh.set(func(f *fakeGitHub) { f.state = "closed" })
	p.poll(w.ID)
	if got := bodies(p.notices(w.ID)); !slices.Equal(got, []string{"closed: PR #7 closed without merging on GitHub"}) {
		t.Fatalf("notices = %q", got)
	}
	if got := p.watchState(w.ID); got.State != protocol.WatchDone || got.NextCheckAt != nil {
		t.Fatalf("state = %s, next_check_at = %v; want done and no more polls", got.State, got.NextCheckAt)
	}
}

func TestRunPRUnreadableDoesNotHideAnotherPRsMerge(t *testing.T) {
	p := newPRHarness(t)
	run := p.run(p.ada, "Fix login")
	for _, pr := range []store.RunPullRequest{
		{Repository: "acme/gone", Number: 1, URL: "https://github.com/acme/gone/pull/1"},
		{Repository: "acme/app", Number: 7, URL: "https://github.com/acme/app/pull/7"},
	} {
		pr.RunID, pr.State, pr.ObservedAt = run.ID, "open", time.Now()
		if err := p.db.RecordRunPullRequest(p.ctx, &pr); err != nil {
			t.Fatal(err)
		}
	}
	w := p.watchOn(p.ada, runTarget(run), protocol.WatchPRMerged).Watch
	merged := p.stamp()
	p.gh.set(func(f *fakeGitHub) { f.state, f.mergedAt = "closed", merged })
	p.clock.advance(PRPollIdle)
	p.eventually("the merge of the readable pull request", func() bool { return len(p.notices(w.ID)) == 1 })
	if got := bodies(p.notices(w.ID)); got[0] != "pr_merged: PR #7 merged on GitHub" {
		t.Fatalf("notices = %q", got)
	}
	if got := p.watchState(w.ID); got.State != protocol.WatchError || !strings.Contains(got.Error, "acme/gone#1 is not reachable") {
		t.Fatalf("state = %s %q, want the unreadable pull request's error", got.State, got.Error)
	}
}
