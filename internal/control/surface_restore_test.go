package control

import (
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

type surfaceRecords struct {
	leases map[Surface]SurfaceLease
	fail   error
}

func (r *surfaceRecords) persist(_ domain.RunID, surface Surface, lease SurfaceLease) error {
	if r.fail != nil {
		return r.fail
	}
	r.leases[surface] = lease
	return nil
}

// restart builds the next server process's service from what the last one
// recorded.
func (r *surfaceRecords) restart(t *testing.T, clock *testClock) *Service {
	t.Helper()
	s := New(Config{Now: clock.Now, PersistSurface: r.persist})
	for surface, lease := range r.leases {
		if err := s.RestoreSurface("run", surface, lease); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestRestoredMemberSurfaceAdmitsOnlyItsHolderWithinTheWindow(t *testing.T) {
	clock := newTestClock()
	records := &surfaceRecords{leases: map[Surface]SurfaceLease{}}
	s := New(Config{Now: clock.Now, PersistSurface: records.persist})
	surface := Surface{Kind: SurfaceTerminal, ID: "shell", Incarnation: "one"}
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	human := Principal{Kind: PrincipalMember, MemberID: "member"}
	other := Principal{Kind: PrincipalMember, MemberID: "other"}
	held := acquireTestSurface(t, s, "run", surface, human, "tab")

	s = records.restart(t, clock)
	status, ok := s.SurfaceStatus("run", surface)
	if !ok || status.Principal != human || status.SessionID != "tab" || status.Generation != held.Generation || status.Connected ||
		!status.ExpiresAt.Equal(clock.Now().Add(DefaultReconnectWindow)) || !status.AcquiredAt.Equal(held.AcquiredAt) {
		t.Fatalf("restored = %+v, %v", status, ok)
	}
	for name, try := range map[string]func() error{
		"other session":     func() error { return s.AdmitSurface("run", surface, human, "other", held.Generation, allowSurface) },
		"raised generation": func() error { return s.AdmitSurface("run", surface, human, "tab", held.Generation+1, allowSurface) },
		"other member":      func() error { return s.AdmitSurface("run", surface, other, "tab", held.Generation, allowSurface) },
		"the agent":         func() error { return s.AdmitSurface("run", surface, agent, "tab", held.Generation, allowSurface) },
	} {
		if err := try(); !errors.Is(err, ErrStale) {
			t.Errorf("%s admitted = %v", name, err)
		}
	}
	if _, _, err := s.AcquireSurface("run", surface, agent, "agent", false, 0, allowSurface); !errors.Is(err, ErrOccupied) {
		t.Fatalf("unforced acquire of a restored lease = %v", err)
	}
	denied := errors.New("denied")
	if err := s.AdmitSurface("run", surface, human, "tab", held.Generation, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("denied admission = %v", err)
	}
	if after, _ := s.SurfaceStatus("run", surface); after.Connected {
		t.Fatal("a denied admission reconnected the restored lease")
	}
	clock.Advance(DefaultReconnectWindow / 2)
	if err := s.AdmitSurface("run", surface, human, "tab", held.Generation, allowSurface); err != nil {
		t.Fatalf("holder refused after restart: %v", err)
	}
	clock.Advance(2 * DefaultReconnectWindow)
	status, ok = s.SurfaceStatus("run", surface)
	if !ok || !status.Connected || status.Generation != held.Generation || !status.ExpiresAt.IsZero() {
		t.Fatalf("readmitted lease = %+v, %v", status, ok)
	}

	if _, _, err := s.AcquireSurface("run", surface, agent, "agent", true, 0, allowSurface); !errors.Is(err, ErrAgentTakeover) {
		t.Fatalf("agent forced a restored member lease = %v", err)
	}
	taken, displaced, err := s.AcquireSurface("run", surface, other, "other-tab", true, 0, allowSurface)
	if err != nil || displaced == nil || displaced.Principal != human || taken.Generation <= held.Generation {
		t.Fatalf("takeover after restart = %+v, %+v, %v", taken, displaced, err)
	}
	if got := records.leases[surface]; got.Generation != taken.Generation || got.Holder == nil || got.Holder.MemberID != "other" || got.Holder.Kind != PrincipalMember {
		t.Fatalf("takeover record = %+v", got)
	}
	s = records.restart(t, clock)
	if err := s.AdmitSurface("run", surface, human, "tab", held.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("displaced member admitted after a second restart = %v", err)
	}
	if err := s.AdmitSurface("run", surface, other, "other-tab", taken.Generation, allowSurface); err != nil {
		t.Fatalf("new holder refused after restart: %v", err)
	}
}

func TestRestoredAgentSurfaceOutlastsTheReconnectWindow(t *testing.T) {
	clock := newTestClock()
	records := &surfaceRecords{leases: map[Surface]SurfaceLease{}}
	s := New(Config{Now: clock.Now, PersistSurface: records.persist})
	surface := Surface{Kind: SurfaceTerminal, ID: "shell", Incarnation: "one"}
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	human := Principal{Kind: PrincipalMember, MemberID: "member"}
	held := acquireTestSurface(t, s, "run", surface, agent, "agent")

	s = records.restart(t, clock)
	clock.Advance(4 * DefaultReconnectWindow)
	status, ok := s.SurfaceStatus("run", surface)
	if !ok || status.Principal != agent || !status.Connected || status.Generation != held.Generation || !status.ExpiresAt.IsZero() {
		t.Fatalf("restored agent lease = %+v, %v", status, ok)
	}
	if err := s.AdmitSurface("run", surface, agent, "agent", held.Generation, allowSurface); err != nil {
		t.Fatalf("agent refused a minute after the restart: %v", err)
	}
	if err := s.AdmitSurface("run", surface, agent, "other", held.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("other agent session admitted = %v", err)
	}
	if _, _, err := s.AcquireSurface("run", surface, agent, "other", false, 0, allowSurface); !errors.Is(err, ErrOccupied) {
		t.Fatalf("second agent session acquired = %v", err)
	}
	taken, displaced, err := s.AcquireSurface("run", surface, human, "tab", true, 0, allowSurface)
	if err != nil || displaced == nil || displaced.Principal != agent || taken.Generation <= held.Generation {
		t.Fatalf("human takeover of a restored agent lease = %+v, %+v, %v", taken, displaced, err)
	}
	if err := s.AdmitSurface("run", surface, agent, "agent", held.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("displaced agent admitted = %v", err)
	}
}

func TestRestoredSurfaceReacquiresExpiresAndNeverReusesAGeneration(t *testing.T) {
	clock := newTestClock()
	records := &surfaceRecords{leases: map[Surface]SurfaceLease{}}
	s := New(Config{Now: clock.Now, PersistSurface: records.persist})
	tab := Surface{Kind: SurfaceTerminal, ID: "tab", Incarnation: "one"}
	idle := Surface{Kind: SurfaceTerminal, ID: "idle", Incarnation: "one"}
	freed := Surface{Kind: SurfaceTerminal, ID: "freed", Incarnation: "one"}
	human := Principal{Kind: PrincipalMember, MemberID: "member"}
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	tabLease := acquireTestSurface(t, s, "run", tab, human, "browser-tab")
	idleLease := acquireTestSurface(t, s, "run", idle, human, "idle-tab")
	freedLease := acquireTestSurface(t, s, "run", freed, agent, "agent")
	if err := s.ReleaseSurface("run", freed, agent, "agent", freedLease.Generation, allowSurface); err != nil {
		t.Fatal(err)
	}
	if got := records.leases[freed]; got.Holder != nil || got.Generation <= freedLease.Generation {
		t.Fatalf("release record = %+v", got)
	}

	s = records.restart(t, clock)
	if _, ok := s.SurfaceStatus("run", freed); ok {
		t.Fatal("restart restored a released lease")
	}
	if err := s.AdmitSurface("run", freed, agent, "agent", freedLease.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("released lease admitted after restart = %v", err)
	}
	if again := acquireTestSurface(t, s, "run", freed, agent, "agent"); again.Generation <= freedLease.Generation+1 {
		t.Fatalf("reused generation %d after release at %d", again.Generation, freedLease.Generation)
	}
	resumed, _, err := s.AcquireSurface("run", tab, human, "browser-tab", false, tabLease.Generation, allowSurface)
	if err != nil || !resumed.Connected || resumed.Generation <= tabLease.Generation {
		t.Fatalf("browser reconnect after restart = %+v, %v", resumed, err)
	}
	if err := s.AdmitSurface("run", tab, human, "browser-tab", tabLease.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("pre-restart generation admitted after reconnect = %v", err)
	}
	clock.Advance(DefaultReconnectWindow)
	if _, ok := s.SurfaceStatus("run", idle); ok {
		t.Fatal("restored lease outlived its reconnect window")
	}
	if got := records.leases[idle]; got.Holder != nil || got.Generation != idleLease.Generation {
		t.Fatalf("expiry record = %+v", got)
	}
	if err := s.AdmitSurface("run", idle, human, "idle-tab", idleLease.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("expired restored lease admitted = %v", err)
	}
	if next := acquireTestSurface(t, s, "run", idle, agent, "agent"); next.Generation <= idleLease.Generation {
		t.Fatalf("reused generation %d after expiry of %d", next.Generation, idleLease.Generation)
	}
}

func TestRestoredSurfaceIsFencedByRevocation(t *testing.T) {
	clock := newTestClock()
	records := &surfaceRecords{leases: map[Surface]SurfaceLease{}}
	s := New(Config{Now: clock.Now, PersistSurface: records.persist})
	surface := Surface{Kind: SurfaceTerminal, ID: "shell", Incarnation: "one"}
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	held := acquireTestSurface(t, s, "run", surface, agent, "agent")
	s = records.restart(t, clock)
	displaced, err := s.RevokeRunSurfaces("run", func() error { return nil })
	if err != nil || len(displaced) != 1 || displaced[0].Generation != held.Generation {
		t.Fatalf("revoke = %+v, %v", displaced, err)
	}
	if err := s.AdmitSurface("run", surface, agent, "agent", held.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("revoked restored lease admitted = %v", err)
	}
	s = records.restart(t, clock)
	if _, ok := s.SurfaceStatus("run", surface); ok {
		t.Fatal("restart restored a revoked lease")
	}
}

func TestFailedSurfaceRecordKeepsTheCurrentHolder(t *testing.T) {
	clock := newTestClock()
	records := &surfaceRecords{leases: map[Surface]SurfaceLease{}}
	s := New(Config{Now: clock.Now, PersistSurface: records.persist})
	surface := Surface{Kind: SurfaceTerminal, ID: "shell", Incarnation: "one"}
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	human := Principal{Kind: PrincipalMember, MemberID: "member"}
	held := acquireTestSurface(t, s, "run", surface, agent, "agent")
	records.fail = errors.New("disk full")
	if _, _, err := s.AcquireSurface("run", surface, human, "human", true, 0, allowSurface); !errors.Is(err, records.fail) {
		t.Fatalf("unrecorded takeover = %v", err)
	}
	if err := s.AdmitSurface("run", surface, agent, "agent", held.Generation, allowSurface); err != nil {
		t.Fatalf("unrecorded takeover displaced the holder: %v", err)
	}
	if err := s.ReleaseSurface("run", surface, agent, "agent", held.Generation, allowSurface); !errors.Is(err, records.fail) {
		t.Fatalf("unrecorded release = %v", err)
	}
	if err := s.AdmitSurface("run", surface, agent, "agent", held.Generation, allowSurface); !errors.Is(err, ErrStale) {
		t.Fatalf("an unrecorded release left the lease admitted: %v", err)
	}
	records.fail = nil
	if next := acquireTestSurface(t, s, "run", surface, human, "human"); next.Generation <= held.Generation+1 {
		t.Fatalf("generation %d reused after release at %d", next.Generation, held.Generation)
	}
}

func TestRestoreSurfaceRejectsInvalidAndUsedSurfaces(t *testing.T) {
	s := New(Config{})
	surface := Surface{Kind: SurfaceTerminal, ID: "shell", Incarnation: "one"}
	for name, record := range map[string]SurfaceLease{
		"zero generation":   {Holder: &SurfaceHolder{Kind: PrincipalRunAgent, SessionID: "agent"}},
		"member without id": {Generation: 1, Holder: &SurfaceHolder{Kind: PrincipalMember, SessionID: "s"}},
		"agent with member": {Generation: 1, Holder: &SurfaceHolder{Kind: PrincipalRunAgent, MemberID: "m", SessionID: "s"}},
		"unknown kind":      {Generation: 1, Holder: &SurfaceHolder{Kind: "admin", SessionID: "s"}},
		"empty session":     {Generation: 1, Holder: &SurfaceHolder{Kind: PrincipalRunAgent}},
	} {
		if err := s.RestoreSurface("run", surface, record); err == nil {
			t.Errorf("%s restored", name)
		}
	}
	if _, ok := s.SurfaceStatus("run", surface); ok {
		t.Fatal("an invalid record installed a lease")
	}
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	acquireTestSurface(t, s, "run", surface, agent, "agent")
	if err := s.RestoreSurface("run", surface, SurfaceLease{Generation: 9, Holder: &SurfaceHolder{Kind: PrincipalMember, MemberID: "m", SessionID: "s"}}); !errors.Is(err, ErrOccupied) {
		t.Fatalf("restore over a live surface = %v", err)
	}
}
