package edgeagent

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

var testOwner = edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo"}

// openState opens the state for edgeURL under dataDir.
func openState(t *testing.T, dataDir, edgeURL string) *State {
	t.Helper()
	s, err := OpenState(dataDir, edgeURL)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func issue(t *testing.T, s *State, at time.Time) string {
	t.Helper()
	code, _, err := s.IssueClaimCode(edgeproto.ServerID(newHostKey(t).PublicKey()), at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func wrongCode(code string) string {
	last := "a"
	if strings.HasSuffix(code, "a") {
		last = "b"
	}
	return code[:len(code)-1] + last
}

func TestClaimCodeAllowsFiveAttempts(t *testing.T) {
	s := openState(t, t.TempDir(), "https://edge.example.test")
	now := time.Now()
	code := issue(t, s, now)
	claims := 0
	claim := func() error { claims++; return nil }
	for i := 1; i <= edgeproto.ClaimCodeAttempts; i++ {
		want := edgeproto.RefusalClaimWrong
		if i == edgeproto.ClaimCodeAttempts {
			want = edgeproto.RefusalClaimExhausted
		}
		if err := s.attemptClaim(wrongCode(code), testOwner, now, claim); !errors.Is(err, want) {
			t.Fatalf("attempt %d: %v, want %v", i, err, want)
		}
	}
	if err := s.attemptClaim(code, testOwner, now, claim); !errors.Is(err, edgeproto.RefusalClaimExhausted) {
		t.Fatalf("right code after five wrong attempts: %v, want exhausted", err)
	}
	if claims != 0 {
		t.Fatalf("claim ran %d times", claims)
	}
	c, _, _ := s.ClaimCode()
	if c.Hash != "" {
		t.Error("an exhausted code kept its hash")
	}
}

func TestClaimCodeExpires(t *testing.T) {
	s := openState(t, t.TempDir(), "https://edge.example.test")
	issued := time.Now()
	code := issue(t, s, issued)
	late := issued.Add(edgeproto.ClaimCodeTTL + time.Second)
	if err := s.attemptClaim(code, testOwner, late, func() error { return nil }); !errors.Is(err, edgeproto.RefusalClaimExpired) {
		t.Fatalf("expired code: %v", err)
	}
	if err := s.attemptClaim(code, testOwner, issued, func() error { return nil }); err == nil {
		t.Fatal("an expired code claimed after the clock went back")
	}
}

func TestClaimCodeIsUsedOnce(t *testing.T) {
	dir := t.TempDir()
	s := openState(t, dir, "https://edge.example.test")
	now := time.Now()
	code := issue(t, s, now)
	raw, err := os.ReadFile(s.path(claimFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), code[edgeproto.ServerIDLength+1:]) {
		t.Fatal("the claim code's secret is stored in the clear")
	}
	if err := s.attemptClaim("  "+strings.ToUpper(code)+"\n", testOwner, now, func() error { return nil }); err != nil {
		t.Fatalf("right code as a person typed it: %v", err)
	}
	if owner, _ := s.Owner(); owner == nil || *owner != testOwner {
		t.Fatalf("owner = %+v", owner)
	}
	if _, ok, _ := s.ClaimCode(); ok {
		t.Error("a used code was kept")
	}
	if err := s.attemptClaim(code, testOwner, now, func() error { return nil }); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("second claim: %v, want already claimed", err)
	}
}

func TestFailedClaimKeepsTheCodeButSpendsTheAttempt(t *testing.T) {
	s := openState(t, t.TempDir(), "https://edge.example.test")
	now := time.Now()
	code := issue(t, s, now)
	boom := errors.New("store is read-only")
	if err := s.attemptClaim(code, testOwner, now, func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	c, ok, _ := s.ClaimCode()
	if !ok || !c.Usable(now) || c.AttemptsLeft != edgeproto.ClaimCodeAttempts-1 {
		t.Fatalf("claim code after a failed claim: %+v", c)
	}
	if owner, _ := s.Owner(); owner != nil {
		t.Fatal("a failed claim recorded an owner")
	}
}

func claimGrant(t *testing.T, a *Agent, connID string) edgeproto.Grant {
	g := sshGrant(t, a, connID)
	g.Kind, g.Account = edgeproto.KindClaim, testOwner
	return g
}

func nextClaim(t *testing.T, sshd *fakeSSH) fakeClaim {
	t.Helper()
	select {
	case c := <-sshd.claims:
		t.Cleanup(func() { close(c.done) })
		return c
	case <-time.After(waitFor):
		t.Fatal("the claim connection did not reach sshd")
		return fakeClaim{}
	}
}

// A claim connection reaches sshd with its grant and the agent's check of
// the code the client presents inside SSH. The edge relays that SSH
// connection and never sees the code.
func TestClaimOverTheEdge(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	code := issueFor(t, a)
	connID := edgeproto.NewConnID()
	edge.openKind(ec, edgeproto.KindClaim, connID, edge.grant(t, claimGrant(t, a, connID)))
	if r := expect[edgeproto.OpenResult](t, ec); r.ConnID != connID || r.Error != "" {
		t.Fatalf("claim open result %+v, want success", r)
	}
	// Nothing reads the edge side of the data socket, so it closes before
	// Run is stopped, as in TestConnectionLimit.
	data := edge.nextData(t)
	t.Cleanup(func() { _ = data.CloseNow() })
	c := nextClaim(t, sshd)
	if c.grant.ConnID != connID || c.grant.Kind != edgeproto.KindClaim {
		t.Fatalf("sshd got claim grant %+v", c.grant)
	}
	if err := c.attempt(wrongCode(code), func() error { t.Fatal("a wrong code ran the claim"); return nil }); !errors.Is(err, edgeproto.RefusalClaimWrong) {
		t.Fatalf("wrong code: %v", err)
	}
	claimed := false
	if err := c.attempt(code, func() error { claimed = true; return nil }); err != nil || !claimed {
		t.Fatalf("right code: %v, claimed %v", err, claimed)
	}
	got := expect[edgeproto.Claimed](t, ec)
	if got.ConnID != connID || got.Owner != edgeproto.AccountPrincipal(testOwner) {
		t.Fatalf("claimed %+v, want the grant's account for %s", got, connID)
	}
	if owner, _ := a.state.Owner(); owner == nil || *owner != testOwner {
		t.Fatalf("owner = %+v", owner)
	}
}

// A grant is good only for an open of its own kind: a claim grant never
// opens a member connection, and an ssh grant never reaches the claim
// code.
func TestGrantKindMustMatchTheOpen(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	for name, tc := range map[string]struct {
		kind  string
		grant func(connID string) edgeproto.Grant
	}{
		"claim grant on an ssh open": {edgeproto.KindSSH, func(id string) edgeproto.Grant { return claimGrant(t, a, id) }},
		"ssh grant on a claim open":  {edgeproto.KindClaim, func(id string) edgeproto.Grant { return sshGrant(t, a, id) }},
	} {
		connID := edgeproto.NewConnID()
		edge.openKind(ec, tc.kind, connID, edge.grant(t, tc.grant(connID)))
		if r := expect[edgeproto.OpenResult](t, ec); !strings.Contains(r.Error, edgeproto.ErrGrantKind.Error()) {
			t.Fatalf("%s: open result %+v, want %v", name, r, edgeproto.ErrGrantKind)
		}
	}
	select {
	case g := <-sshd.served:
		t.Fatalf("a mismatched grant reached sshd: %+v", g)
	case c := <-sshd.claims:
		close(c.done)
		t.Fatalf("a mismatched grant reached the claim: %+v", c.grant)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAccountDeletedReachesSSH(t *testing.T) {
	_, sshd, _, ec := enrolled(t)
	ec.send(edgeproto.AccountDeleted{Provider: testOwner.Provider, Subject: testOwner.Subject})
	select {
	case got := <-sshd.deleted:
		if got.Provider != testOwner.Provider || got.Subject != testOwner.Subject {
			t.Fatalf("EdgeAccountDeleted(%+v)", got)
		}
	case <-time.After(waitFor):
		t.Fatal("sshd was not told about the deleted account")
	}
}

func TestTransferOwner(t *testing.T) {
	edge := newFakeEdge(t)
	dir := t.TempDir()
	sshd := newFakeSSH()
	next := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-2", Email: "next@example.com"}
	sshd.entries = []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: testOwner.Provider, Subject: testOwner.Subject, Role: "admin"},
		{Kind: edgeproto.EntryMember, Provider: next.Provider, Subject: next.Subject, Role: "admin"},
	}
	a := newAgent(t, edge.srv.URL, dir, sshd)
	if err := a.TransferOwner(next); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("transfer while not connected = %v", err)
	}
	run(t, a)
	ec := edge.nextControl(t)
	waitStatus(t, a.state, func(st Status) bool { return st.Connected })
	if err := a.TransferOwner(next); err == nil || !strings.Contains(err.Error(), "claim code") {
		t.Fatalf("transfer of a server without an owner = %v, want refused", err)
	}
	if err := a.state.write(ownerFile, testOwner); err != nil {
		t.Fatal(err)
	}
	if err := a.TransferOwner(next); err != nil {
		t.Fatal(err)
	}
	if got := expect[edgeproto.OwnerTransferred](t, ec); got.Owner != edgeproto.AccountPrincipal(next) {
		t.Fatalf("owner_transferred %+v", got)
	}
	if owner, _ := a.state.Owner(); owner == nil || *owner != next {
		t.Fatalf("owner after transfer = %+v", owner)
	}
}

// When the owner's identity leaves the server, the server tells the edge
// it has no owner, and only a claim code gives it one again.
func TestOwnerLeavingMakesTheServerOwnerless(t *testing.T) {
	edge := newFakeEdge(t)
	edge.state = edgeproto.StateClaimed
	dir := t.TempDir()
	sshd := newFakeSSH()
	sshd.entries = []edgeproto.DirectoryEntry{{Kind: edgeproto.EntryMember, Provider: testOwner.Provider, Subject: testOwner.Subject, Role: "admin"}}
	a := newAgent(t, edge.srv.URL, dir, sshd)
	if err := a.state.write(ownerFile, testOwner); err != nil {
		t.Fatal(err)
	}
	run(t, a)
	ec := edge.nextControl(t)
	expect[edgeproto.Directory](t, ec)
	if owner, _ := a.state.Owner(); owner == nil {
		t.Fatal("owner forgotten while still a member")
	}
	sshd.setDirectory(nil)
	expect[edgeproto.Ownerless](t, ec)
	if owner, _ := a.state.Owner(); owner != nil {
		t.Fatalf("owner = %+v after its identity left", owner)
	}
}

func issueFor(t *testing.T, a *Agent) string {
	t.Helper()
	code, _, err := a.state.IssueClaimCode(a.ServerID(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}
