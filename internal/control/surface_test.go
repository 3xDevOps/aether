package control

import (
	"errors"
	"testing"
	"time"
)

func allowSurface() error { return nil }

func acquireTestSurface(t *testing.T, service *Service, run string, surface Surface, principal Principal, session string) SurfaceSnapshot {
	t.Helper()
	lease, _, err := service.AcquireSurface(run, surface, principal, session, false, 0, allowSurface)
	if err != nil { t.Fatal(err) }
	return lease
}

func TestDevelopmentSurfacesAndPrimaryHaveIndependentControllers(t *testing.T) {
	s := New(Config{})
	primary, _, err := s.Acquire("run", "member", "primary", false)
	if err != nil { t.Fatal(err) }
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	terminal := Surface{Kind: SurfaceTerminal, ID: "shell-1", Incarnation: "tty-1"}
	browser := Surface{Kind: SurfaceBrowser, ID: "browser", Incarnation: "context-1"}
	tty := acquireTestSurface(t, s, "run", terminal, agent, "agent-tty")
	web := acquireTestSurface(t, s, "run", browser, agent, "agent-browser")
	if tty.Principal.MemberID != "" || tty.Principal.RunID != "run" { t.Fatalf("fabricated human identity: %+v", tty) }
	if err := s.ReleaseSurface("run", terminal, agent, tty.SessionID, tty.Generation, allowSurface); err != nil { t.Fatal(err) }
	if err := s.AdmitSurface("run", browser, agent, web.SessionID, web.Generation, allowSurface); err != nil { t.Fatalf("terminal release fenced browser: %v", err) }
	if err := s.ValidateMember("run", "member", primary.SessionID, primary.Generation); err != nil { t.Fatalf("terminal release fenced primary: %v", err) }
	if err := s.Release("run", "member", primary.SessionID, primary.Generation); err != nil { t.Fatal(err) }
	if err := s.AdmitSurface("run", browser, agent, web.SessionID, web.Generation, allowSurface); err != nil { t.Fatalf("primary release fenced browser: %v", err) }
	if err := s.ReleaseSurface("other-run", browser, agent, web.SessionID, web.Generation, allowSurface); !errors.Is(err, ErrInvalid) { t.Fatalf("foreign agent release = %v", err) }
	wrong := browser
	wrong.Incarnation = "context-2"
	if err := s.ReleaseSurface("run", wrong, agent, web.SessionID, web.Generation, allowSurface); !errors.Is(err, ErrStale) { t.Fatalf("foreign incarnation release = %v", err) }
	if _, present := s.SurfaceStatus("run", browser); !present { t.Fatal("wrong-scope release removed browser") }
}

func TestSurfaceHumanTakeoverFencesAgentWithoutAgentForce(t *testing.T) {
	s := New(Config{})
	surface := Surface{Kind: SurfaceTerminal, ID: "shell", Incarnation: "one"}
	agent := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	human := Principal{Kind: PrincipalMember, MemberID: "member"}
	first := acquireTestSurface(t, s, "run", surface, agent, "agent")
	if _, _, err := s.AcquireSurface("run", surface, agent, "second-agent", false, 0, allowSurface); !errors.Is(err, ErrOccupied) { t.Fatalf("duplicate writer = %v", err) }
	second, displaced, err := s.AcquireSurface("run", surface, human, "human", true, first.Generation, allowSurface)
	if err != nil || displaced == nil || displaced.Principal != agent || second.Generation <= first.Generation { t.Fatalf("takeover = %+v, %+v, %v", second, displaced, err) }
	called := false
	if err := s.AdmitSurface("run", surface, agent, "agent", first.Generation, func() error { called = true; return nil }); !errors.Is(err, ErrStale) || called { t.Fatalf("stale agent accepted: called=%v err=%v", called, err) }
	if _, _, err := s.AcquireSurface("run", surface, agent, "agent", true, second.Generation, allowSurface); !errors.Is(err, ErrAgentTakeover) { t.Fatalf("agent forced human = %v", err) }
	if err := s.ReleaseSurface("run", surface, agent, "agent", first.Generation, allowSurface); !errors.Is(err, ErrStale) { t.Fatalf("stale release = %v", err) }
	if err := s.AdmitSurface("run", surface, human, "human", second.Generation, allowSurface); err != nil { t.Fatalf("human lease lost: %v", err) }
}

func TestSurfaceReconnectAndExpiryFenceOldTransports(t *testing.T) {
	clock := newTestClock()
	s := New(Config{Now: clock.Now})
	surface := Surface{Kind: SurfaceBrowser, ID: "browser", Incarnation: "one"}
	principal := Principal{Kind: PrincipalMember, MemberID: "member"}
	first := acquireTestSurface(t, s, "run", surface, principal, "tab")
	s.DisconnectSurface("run", surface, principal, "tab", first.Generation)
	if err := s.AdmitSurface("run", surface, principal, "tab", first.Generation, allowSurface); !errors.Is(err, ErrStale) { t.Fatalf("disconnected write = %v", err) }
	if _, _, err := s.AcquireSurface("run", surface, principal, "other-tab", false, 0, allowSurface); !errors.Is(err, ErrOccupied) { t.Fatalf("other tab inherited reconnect = %v", err) }
	clock.Advance(DefaultReconnectWindow / 2)
	resumed, displaced, err := s.AcquireSurface("run", surface, principal, "tab", false, first.Generation, allowSurface)
	if err != nil || displaced != nil || resumed.Generation != first.Generation || !resumed.Connected || !resumed.ExpiresAt.IsZero() { t.Fatalf("resume = %+v %v %v", resumed, displaced, err) }
	s.DisconnectSurface("run", surface, principal, "tab", resumed.Generation)
	clock.Advance(DefaultReconnectWindow)
	fresh := acquireTestSurface(t, s, "run", surface, principal, "other-tab")
	if fresh.Generation <= resumed.Generation { t.Fatalf("reused expired generation: %+v", fresh) }
	s.DisconnectSurface("run", surface, principal, "tab", resumed.Generation)
	if err := s.AdmitSurface("run", surface, principal, fresh.SessionID, fresh.Generation, allowSurface); err != nil { t.Fatalf("stale disconnect hit replacement: %v", err) }
}

func TestSurfaceAdmissionSerializesTakeoverButNotOtherSurface(t *testing.T) {
	s := New(Config{})
	a := Surface{Kind: SurfaceTerminal, ID: "a", Incarnation: "one"}
	b := Surface{Kind: SurfaceTerminal, ID: "b", Incarnation: "one"}
	principal := Principal{Kind: PrincipalRunAgent, RunID: "run"}
	first := acquireTestSurface(t, s, "run", a, principal, "agent")
	second := acquireTestSurface(t, s, "run", b, principal, "agent")
	entered := make(chan struct{})
	unblock := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() { writeDone <- s.AdmitSurface("run", a, principal, "agent", first.Generation, func() error { close(entered); <-unblock; return nil }) }()
	<-entered
	otherDone := make(chan error, 1)
	go func() { otherDone <- s.AdmitSurface("run", b, principal, "agent", second.Generation, allowSurface) }()
	select {
	case err := <-otherDone:
		if err != nil { close(unblock); t.Fatal(err) }
	case <-time.After(time.Second):
		close(unblock)
		t.Fatal("one surface blocked another's admission")
	}
	takeoverDone := make(chan error, 1)
	go func() {
		_, _, err := s.AcquireSurface("run", a, Principal{Kind: PrincipalMember, MemberID: "member"}, "human", true, first.Generation, allowSurface)
		takeoverDone <- err
	}()
	close(unblock)
	if err := <-writeDone; err != nil { t.Fatal(err) }
	if err := <-takeoverDone; err != nil { t.Fatal(err) }
	if err := s.AdmitSurface("run", a, principal, "agent", first.Generation, allowSurface); !errors.Is(err, ErrStale) { t.Fatalf("takeover left admitted generation: %v", err) }
}

func TestRunRevocationFencesSurfacesAndFailedRevocationPreservesThem(t *testing.T) {
	s := New(Config{})
	surface := Surface{Kind: SurfaceTerminal, ID: "shell", Incarnation: "one"}
	principal := Principal{Kind: PrincipalMember, MemberID: "member"}
	primary, _, err := s.Acquire("run", "member", "primary", false)
	if err != nil { t.Fatal(err) }
	first := acquireTestSurface(t, s, "run", surface, principal, "shell")
	other := acquireTestSurface(t, s, "other", surface, principal, "shell")
	denied := errors.New("permission withdrawn")
	if _, err := s.AdmitRevoke("run", func() error { return denied }); !errors.Is(err, denied) { t.Fatal(err) }
	if err := s.AdmitSurface("run", surface, principal, first.SessionID, first.Generation, allowSurface); err != nil { t.Fatalf("failed revocation changed surface: %v", err) }
	if _, err := s.AdmitRevoke("run", allowSurface); err != nil { t.Fatal(err) }
	if err := s.AdmitSurface("run", surface, principal, first.SessionID, first.Generation, allowSurface); !errors.Is(err, ErrStale) { t.Fatalf("run revoke missed surface: %v", err) }
	if err := s.Validate("run", primary.SessionID, primary.Generation); !errors.Is(err, ErrStale) { t.Fatalf("run revoke missed primary: %v", err) }
	if err := s.AdmitSurface("other", surface, principal, other.SessionID, other.Generation, allowSurface); err != nil { t.Fatalf("run revoke crossed runs: %v", err) }
	fresh := acquireTestSurface(t, s, "run", surface, principal, "shell")
	if fresh.Generation <= first.Generation { t.Fatal("revocation reused old generation") }
	s.Fence("run")
	if err := s.AdmitSurface("run", surface, principal, fresh.SessionID, fresh.Generation, allowSurface); !errors.Is(err, ErrStale) { t.Fatalf("Fence missed surface: %v", err) }
}

func TestSurfaceFailedAdmissionAndInvalidPrincipalsDoNotDisplaceWriter(t *testing.T) {
	s := New(Config{})
	surface := Surface{Kind: SurfaceBrowser, ID: "browser", Incarnation: "one"}
	human := Principal{Kind: PrincipalMember, MemberID: "member"}
	first := acquireTestSurface(t, s, "run", surface, human, "first")
	for _, principal := range []Principal{
		{Kind: PrincipalRunAgent, RunID: "other"},
		{Kind: PrincipalRunAgent, RunID: "run", MemberID: "fake-human"},
		{Kind: PrincipalMember, RunID: "run", MemberID: "member"},
		{},
	} {
		if _, _, err := s.AcquireSurface("run", surface, principal, "invalid", false, 0, allowSurface); !errors.Is(err, ErrInvalid) { t.Fatalf("invalid principal %+v = %v", principal, err) }
	}
	denied := errors.New("not authorized")
	if _, _, err := s.AcquireSurface("run", surface, human, "second", true, first.Generation, func() error { return denied }); !errors.Is(err, denied) { t.Fatal(err) }
	if err := s.ReleaseSurface("run", surface, human, "first", first.Generation, func() error { return denied }); !errors.Is(err, denied) { t.Fatal(err) }
	if _, err := s.RevokeSurface("run", surface, func() error { return denied }); !errors.Is(err, denied) { t.Fatal(err) }
	if err := s.AdmitSurface("run", surface, human, "first", first.Generation, allowSurface); err != nil { t.Fatalf("failed authorization displaced writer: %v", err) }
}
