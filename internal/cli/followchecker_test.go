package cli

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

var askedMu sync.Mutex

type fakeChecker struct {
	channel string
	latest  string
	asked   *[]string
}

func (f fakeChecker) Check(context.Context) (selfupdate.Check, error) {
	askedMu.Lock()
	*f.asked = append(*f.asked, f.channel)
	askedMu.Unlock()
	return selfupdate.Check{Latest: f.latest}, nil
}

func (f fakeChecker) CheckFresh(ctx context.Context) (selfupdate.Check, error) { return f.Check(ctx) }
func (f fakeChecker) BaseURL() string                                          { return "" }

func followed(channel func(context.Context) string, latest map[string]string, asked *[]string) *FollowedChecker {
	return &FollowedChecker{
		channel: channel,
		now:     time.Now,
		build: func(c string) ReleaseChecker {
			return fakeChecker{channel: c, latest: latest[c], asked: asked}
		},
	}
}

func TestFollowedCheckerFollowsTheServersChannel(t *testing.T) {
	var asked []string
	f := followed(func(context.Context) string { return "nightly" }, map[string]string{"nightly": "v0.9.0-nightly.3", "stable": "v0.8.7"}, &asked)
	got, err := f.Check(t.Context())
	if err != nil || got.Latest != "v0.9.0-nightly.3" {
		t.Fatalf("Check() = %+v, %v; want the nightly release", got, err)
	}
	if len(asked) != 1 || asked[0] != "nightly" {
		t.Fatalf("asked channels %v; want only nightly", asked)
	}
}

func TestFollowedCheckerChecksStableThenBetaWithNoServer(t *testing.T) {
	var asked []string
	none := func(context.Context) string { return "" }

	f := followed(none, map[string]string{"stable": "v0.8.7", "beta": "v0.8.7-beta.14"}, &asked)
	if got, _ := f.Check(t.Context()); got.Latest != "v0.8.7" {
		t.Fatalf("with a stable release got %q; want v0.8.7", got.Latest)
	}
	if len(asked) != 1 {
		t.Fatalf("asked channels %v; want only stable", asked)
	}

	asked = nil
	f = followed(none, map[string]string{"beta": "v0.8.7-beta.14"}, &asked)
	if got, _ := f.Check(t.Context()); got.Latest != "v0.8.7-beta.14" {
		t.Fatalf("before the first stable got %q; want the beta release", got.Latest)
	}
	if len(asked) != 2 || asked[0] != "stable" || asked[1] != "beta" {
		t.Fatalf("asked channels %v; want stable, then beta", asked)
	}
}

func TestFollowedCheckerKeepsTheLastChannelWhenTheServerIsDown(t *testing.T) {
	var asked []string
	answers := []string{"beta", ""}
	reads := 0
	f := followed(func(context.Context) string {
		a := answers[min(reads, len(answers)-1)]
		reads++
		return a
	}, map[string]string{"beta": "v0.8.7-beta.14", "stable": "v0.8.7"}, &asked)

	if _, err := f.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CheckFresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("read the channel %d times; want a fresh check to ask the server again", reads)
	}
	if len(asked) != 2 || asked[1] != "beta" {
		t.Fatalf("asked channels %v; want beta both times", asked)
	}
}

func TestFollowedCheckerDoesNotRedialAServerThatAnswersNothing(t *testing.T) {
	var asked []string
	reads := 0
	f := followed(func(context.Context) string { reads++; return "" }, map[string]string{"stable": "v0.8.7"}, &asked)
	for range 4 {
		if _, err := f.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("read the channel %d times for a server that gave none; want 1 per period", reads)
	}
}

func TestFollowedCheckerReadsOnceForConcurrentChecks(t *testing.T) {
	var asked []string
	var reads atomic.Int32
	release := make(chan struct{})
	f := followed(func(context.Context) string {
		reads.Add(1)
		<-release
		return "beta"
	}, map[string]string{"beta": "v0.8.7-beta.14"}, &asked)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.Check(t.Context())
		}()
	}
	for reads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := reads.Load(); got != 1 {
		t.Fatalf("read the channel %d times for 5 concurrent checks; want 1", got)
	}
}

func TestFollowedCheckerAsksTheServerOncePerPeriod(t *testing.T) {
	var asked []string
	reads := 0
	f := followed(func(context.Context) string { reads++; return "beta" }, map[string]string{"beta": "v0.8.7-beta.14"}, &asked)
	for range 3 {
		if _, err := f.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("read the channel %d times in a row; want 1", reads)
	}
}
