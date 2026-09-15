package control

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestAcquireRejectsSecondTabAndForceTakeoverFencesOldGeneration(t *testing.T) {
	clock := newTestClock()
	service := New(Config{Now: clock.Now})

	first, displaced, err := service.Acquire("run-1", "member-1", "tab-a", false)
	if err != nil || displaced != nil {
		t.Fatalf("first acquire = %+v, %v, displaced=%+v", first, err, displaced)
	}
	if first.Generation != 1 || !first.Connected || !first.ExpiresAt.IsZero() {
		t.Fatalf("first snapshot = %+v", first)
	}

	if _, _, acquireErr := service.Acquire("run-1", "member-1", "tab-b", false); !errors.Is(acquireErr, ErrOccupied) {
		t.Fatalf("unforced second tab error = %v, want ErrOccupied", acquireErr)
	}

	second, displaced, err := service.Acquire("run-1", "member-1", "tab-b", true)
	if err != nil || displaced == nil {
		t.Fatalf("forced takeover = %+v, %v, displaced=%+v", second, err, displaced)
	}
	if displaced.SessionID != "tab-a" || displaced.Generation != first.Generation || displaced.MemberID != "member-1" {
		t.Fatalf("displaced snapshot = %+v", displaced)
	}
	if second.Generation != first.Generation+1 || second.SessionID != "tab-b" {
		t.Fatalf("takeover snapshot = %+v, first generation=%d", second, first.Generation)
	}
	if err := service.Validate("run-1", "tab-a", first.Generation); !errors.Is(err, ErrStale) {
		t.Fatalf("old generation validate = %v, want ErrStale", err)
	}
	if err := service.Validate("run-1", "tab-b", second.Generation); err != nil {
		t.Fatalf("new generation validate = %v", err)
	}
}

func TestAcquireForcedReconnectFencesSameSessionIncarnation(t *testing.T) {
	service := New(Config{})
	first, _, err := service.Acquire("run-1", "member-1", "tab-a", false)
	if err != nil {
		t.Fatal(err)
	}

	reconnected, displaced, err := service.AcquireAuthorized(
		"run-1", "member-1", "tab-a", true, first.Generation, nil,
	)
	if err != nil || displaced == nil {
		t.Fatalf("forced reconnect = %+v, %v, displaced=%+v", reconnected, err, displaced)
	}
	if displaced.Generation != first.Generation ||
		reconnected.Generation != first.Generation+1 ||
		reconnected.SessionID != first.SessionID {
		t.Fatalf("forced reconnect = %+v, displaced=%+v, first=%+v", reconnected, displaced, first)
	}
	if err := service.Validate("run-1", "tab-a", first.Generation); !errors.Is(err, ErrStale) {
		t.Fatalf("old incarnation validate = %v, want ErrStale", err)
	}
	if err := service.Validate("run-1", "tab-a", reconnected.Generation); err != nil {
		t.Fatalf("new incarnation validate = %v", err)
	}
}

func TestAcquireReconnectsOnlySameSessionWithinWindow(t *testing.T) {
	clock := newTestClock()
	service := New(Config{Now: clock.Now})

	first, _, err := service.Acquire("run-1", "member-1", "tab-a", false)
	if err != nil {
		t.Fatal(err)
	}
	service.Disconnect("run-1", "tab-a", first.Generation)
	status, ok := service.Status("run-1")
	if !ok || status.Connected || !status.ExpiresAt.Equal(clock.Now().Add(DefaultReconnectWindow)) {
		t.Fatalf("disconnected status = %+v, ok=%v", status, ok)
	}
	if validateErr := service.Validate("run-1", "tab-a", first.Generation); !errors.Is(validateErr, ErrStale) {
		t.Fatalf("disconnected validate = %v, want ErrStale", validateErr)
	}

	clock.Advance(DefaultReconnectWindow / 2)
	resumed, displaced, err := service.Acquire("run-1", "member-1", "tab-a", false)
	if err != nil || displaced != nil {
		t.Fatalf("resume = %+v, %v, displaced=%+v", resumed, err, displaced)
	}
	if resumed.Generation != first.Generation || !resumed.Connected || !resumed.ExpiresAt.IsZero() || !resumed.AcquiredAt.Equal(first.AcquiredAt) {
		t.Fatalf("resumed snapshot = %+v, first=%+v", resumed, first)
	}

	service.Disconnect("run-1", "tab-a", resumed.Generation)
	clock.Advance(DefaultReconnectWindow)
	if _, ok := service.Status("run-1"); ok {
		t.Fatal("expired disconnected lease still has status")
	}
	if validateErr := service.Validate("run-1", "tab-a", resumed.Generation); !errors.Is(validateErr, ErrStale) {
		t.Fatalf("expired validate = %v, want ErrStale", validateErr)
	}

	fresh, displaced, err := service.Acquire("run-1", "member-1", "tab-a", false)
	if err != nil || displaced != nil {
		t.Fatalf("post-expiry acquire = %+v, %v, displaced=%+v", fresh, err, displaced)
	}
	if fresh.Generation != first.Generation+1 || !fresh.Connected || !fresh.AcquiredAt.Equal(clock.Now()) {
		t.Fatalf("post-expiry snapshot = %+v, first=%+v", fresh, first)
	}
}

func TestReleaseAndFenceAdvanceGeneration(t *testing.T) {
	clock := newTestClock()
	service := New(Config{Now: clock.Now})

	first, _, err := service.Acquire("run-release", "member-1", "session-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if releaseErr := service.Release("run-release", "member-1", "session-a", first.Generation); releaseErr != nil {
		t.Fatalf("release = %v", releaseErr)
	}
	if _, ok := service.Status("run-release"); ok {
		t.Fatal("released run still has status")
	}
	if releaseErr := service.Release("run-release", "member-1", "session-a", first.Generation); !errors.Is(releaseErr, ErrStale) {
		t.Fatalf("second release = %v, want ErrStale", releaseErr)
	}
	if _, _, acquireErr := service.Acquire("run-release", "member-1", "session-b", false); acquireErr != nil {
		t.Fatal(acquireErr)
	}
	second, _, err := service.Acquire("run-fence", "member-1", "session-a", false)
	if err != nil {
		t.Fatal(err)
	}
	displaced := service.Fence("run-fence")
	if displaced == nil || displaced.Generation != second.Generation || displaced.SessionID != second.SessionID {
		t.Fatalf("fence displaced = %+v, want %+v", displaced, second)
	}
	if _, ok := service.Status("run-fence"); ok {
		t.Fatal("fenced run still has status")
	}
	fresh, _, err := service.Acquire("run-fence", "member-1", "session-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation != second.Generation+2 {
		t.Fatalf("fence generation = %d, want %d", fresh.Generation, second.Generation+2)
	}
}

func TestReleaseAdmittedCommitsOnlyAfterAdmission(t *testing.T) {
	service := New(Config{})
	held, _, err := service.Acquire("run-release-admit", "member-1", "session-a", false)
	if err != nil {
		t.Fatal(err)
	}
	err = service.ReleaseAdmitted("run-release-admit", "member-1", held.SessionID, held.Generation, nil)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil admission = %v, want ErrInvalid", err)
	}

	admissionErr := errors.New("replacement admission failed")
	err = service.ReleaseAdmitted("run-release-admit", "member-1", held.SessionID, held.Generation, func() error {
		return admissionErr
	})
	if !errors.Is(err, admissionErr) {
		t.Fatalf("failed admission release = %v, want %v", err, admissionErr)
	}
	if current, ok := service.Status("run-release-admit"); !ok || current != held {
		t.Fatalf("lease after failed admission = %+v/%v, want %+v", current, ok, held)
	}

	admitted := false
	err = service.ReleaseAdmitted("run-release-admit", "member-1", held.SessionID, held.Generation, func() error {
		admitted = true
		return nil
	})
	if err != nil {
		t.Fatalf("successful admitted release = %v", err)
	}
	if !admitted {
		t.Fatal("release did not run replacement admission")
	}
	if _, ok := service.Status("run-release-admit"); ok {
		t.Fatal("admitted release left the old lease")
	}
	fresh, _, err := service.Acquire("run-release-admit", "member-1", "session-b", false)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation != held.Generation+2 {
		t.Fatalf("post-release generation = %d, want %d", fresh.Generation, held.Generation+2)
	}
}

func TestReleaseRequiresMemberAndExplicitGeneration(t *testing.T) {
	clock := newTestClock()
	service := New(Config{Now: clock.Now})
	lease, _, err := service.Acquire("run-release-auth", "member-1", "session-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Release("run-release-auth", "member-2", lease.SessionID, lease.Generation); !errors.Is(err, ErrStale) {
		t.Fatalf("cross-member release = %v, want ErrStale", err)
	}
	if _, ok := service.Status("run-release-auth"); !ok {
		t.Fatal("cross-member release removed the lease")
	}
	if err := service.Release("run-release-auth", "member-1", lease.SessionID, 0); !errors.Is(err, ErrStale) {
		t.Fatalf("omitted generation release = %v, want ErrStale", err)
	}
	if _, ok := service.Status("run-release-auth"); !ok {
		t.Fatal("omitted generation release removed the lease")
	}
	service.Disconnect("run-release-auth", lease.SessionID, lease.Generation)
	if err := service.Release("run-release-auth", "member-1", lease.SessionID, lease.Generation); err != nil {
		t.Fatalf("disconnected holder release = %v", err)
	}
	if _, ok := service.Status("run-release-auth"); ok {
		t.Fatal("disconnected holder release left the lease")
	}
}

func TestConcurrentAcquireHasOneWinner(t *testing.T) {
	service := New(Config{})
	const contenders = 32

	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for i := range contenders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := service.Acquire("run-race", "member", "session-"+string(rune('a'+i)), false)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	wins := 0
	for err := range results {
		if err == nil {
			wins++
			continue
		}
		if !errors.Is(err, ErrOccupied) {
			t.Fatalf("concurrent acquire error = %v, want ErrOccupied", err)
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent acquire winners = %d, want 1", wins)
	}
}

func TestSessionIDBounds(t *testing.T) {
	service := New(Config{MaxSessionIDBytes: 4})
	for _, session := range []string{"", "12345"} {
		if _, _, err := service.Acquire("run-1", "member-1", session, false); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("Acquire session %q error = %v, want ErrInvalidSession", session, err)
		}
	}
	if _, _, err := service.Acquire("run-1", "member-1", "1234", false); err != nil {
		t.Fatalf("maximum-length session rejected: %v", err)
	}
	if err := service.Validate("run-1", "", 1); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Validate empty session error = %v, want ErrInvalidSession", err)
	}
}
func TestGenerationExhaustionNeverReusesLease(t *testing.T) {
	service := New(Config{})
	service.mu.Lock()
	state := service.stateLocked("run-exhausted")
	state.generation = ^uint64(0)
	service.mu.Unlock()

	if _, _, err := service.Acquire("run-exhausted", "member", "session", false); !errors.Is(err, ErrGenerationExhausted) {
		t.Fatalf("Acquire at maximum generation = %v, want ErrGenerationExhausted", err)
	}

	service.mu.Lock()
	state.current = &lease{
		memberID:   "member",
		sessionID:  "session",
		generation: ^uint64(0),
		connected:  true,
	}
	service.mu.Unlock()
	if err := service.Release("run-exhausted", "member", "session", ^uint64(0)); err != nil {
		t.Fatalf("Release maximum generation = %v", err)
	}
	if err := service.Validate("run-exhausted", "session", ^uint64(0)); !errors.Is(err, ErrStale) {
		t.Fatalf("released maximum generation validate = %v, want ErrStale", err)
	}
	if _, _, err := service.Acquire("run-exhausted", "member", "new-session", false); !errors.Is(err, ErrGenerationExhausted) {
		t.Fatalf("Acquire after maximum generation release = %v, want ErrGenerationExhausted", err)
	}
}

func TestAdmitRevokeFencesTheLeaseAtTheMutationBoundary(t *testing.T) {
	service := New(Config{})
	original, _, err := service.Acquire("run-handoff", "old-owner", "old-session", false)
	if err != nil {
		t.Fatal(err)
	}

	mutationEntered := make(chan struct{})
	releaseMutation := make(chan struct{})
	type revokeResult struct {
		displaced *Snapshot
		err       error
	}
	revoked := make(chan revokeResult, 1)
	go func() {
		displaced, revokeErr := service.AdmitRevoke("run-handoff", func() error {
			close(mutationEntered)
			<-releaseMutation
			return nil
		})
		revoked <- revokeResult{displaced: displaced, err: revokeErr}
	}()
	<-mutationEntered

	oldWrite := make(chan error, 1)
	go func() {
		oldWrite <- service.AdmitMember(
			"run-handoff", "old-owner", original.SessionID, original.Generation,
			func() error { return nil },
		)
	}()
	newLease := make(chan Snapshot, 1)
	newLeaseErr := make(chan error, 1)
	go func() {
		next, _, acquireErr := service.Acquire("run-handoff", "new-owner", "new-session", false)
		newLease <- next
		newLeaseErr <- acquireErr
	}()

	close(releaseMutation)
	result := <-revoked
	if result.err != nil {
		t.Fatalf("AdmitRevoke = %v", result.err)
	}
	if result.displaced == nil ||
		result.displaced.SessionID != original.SessionID ||
		result.displaced.Generation != original.Generation {
		t.Fatalf("displaced lease = %+v, want %+v", result.displaced, original)
	}
	if writeErr := <-oldWrite; !errors.Is(writeErr, ErrStale) {
		t.Fatalf("old admission after revoke = %v, want ErrStale", writeErr)
	}
	next := <-newLease
	if acquireErr := <-newLeaseErr; acquireErr != nil {
		t.Fatalf("new owner acquire = %v", acquireErr)
	}
	if next.MemberID != "new-owner" || next.SessionID != "new-session" {
		t.Fatalf("new owner lease = %+v", next)
	}
	if validateErr := service.Validate("run-handoff", next.SessionID, next.Generation); validateErr != nil {
		t.Fatalf("new owner lease was revoked: %v", validateErr)
	}
}

func TestAdmitRevokeFailureKeepsTheCurrentLease(t *testing.T) {
	service := New(Config{})
	current, _, err := service.Acquire("run-failed-handoff", "owner", "session", false)
	if err != nil {
		t.Fatal(err)
	}
	mutationErr := errors.New("transfer failed")
	displaced, revokeErr := service.AdmitRevoke("run-failed-handoff", func() error {
		return mutationErr
	})
	if !errors.Is(revokeErr, mutationErr) || displaced != nil {
		t.Fatalf("failed revoke = displaced %+v, error %v", displaced, revokeErr)
	}
	if validateErr := service.Validate("run-failed-handoff", current.SessionID, current.Generation); validateErr != nil {
		t.Fatalf("failed mutation revoked current lease: %v", validateErr)
	}
}

func TestAdmissionOnOneRunDoesNotBlockAnotherRun(t *testing.T) {
	service := New(Config{})
	if _, _, err := service.Acquire("run-b", "member", "session-b", false); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	admitted := make(chan error, 1)
	go func() {
		admitted <- service.Admit("run-a", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	status := make(chan bool, 1)
	go func() {
		_, ok := service.Status("run-b")
		status <- ok
	}()
	select {
	case ok := <-status:
		if !ok {
			t.Fatal("unrelated run lost its controller")
		}
	case <-time.After(time.Second):
		t.Fatal("run-b status blocked behind run-a admission")
	}

	close(release)
	if err := <-admitted; err != nil {
		t.Fatalf("run-a admission = %v", err)
	}
}
