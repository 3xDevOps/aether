package edgeagent

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

var testOwner = edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo"}

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
	s := OpenState(t.TempDir())
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
	s := OpenState(t.TempDir())
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
	s := OpenState(dir)
	now := time.Now()
	code := issue(t, s, now)
	raw, err := os.ReadFile(s.path(claimFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), code[edgeproto.ClaimPrefixLength+1:]) {
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
	s := OpenState(t.TempDir())
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

func TestClaimOverTheEdge(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	code := issueFor(t, a)
	claimGrant := func(id string) string {
		now := time.Now()
		return edge.grant(t, edgeproto.Grant{
			ServerID: a.ServerID(), ConnID: id, Kind: edgeproto.KindClaim, Account: testOwner,
			DeviceID: "dev-1", IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		})
	}
	// A grant for another claim id is refused before the code is checked.
	id := edgeproto.NewConnID()
	ec.send(edgeproto.Claim{ID: id, Code: code, Grant: claimGrant(edgeproto.NewConnID())})
	if r := expect[edgeproto.ClaimResult](t, ec); !strings.Contains(r.Error, edgeproto.ErrGrantConn.Error()) {
		t.Fatalf("claim with another claim's grant: %+v", r)
	}
	if c, _, _ := a.state.ClaimCode(); c.AttemptsLeft != edgeproto.ClaimCodeAttempts {
		t.Fatalf("a refused grant spent an attempt: %+v", c)
	}

	id = edgeproto.NewConnID()
	ec.send(edgeproto.Claim{ID: id, Code: code, Grant: claimGrant(id)})
	if r := expect[edgeproto.ClaimResult](t, ec); r.ID != id || r.Error != "" {
		t.Fatalf("claim result %+v", r)
	}
	if got := expect[edgeproto.Claimed](t, ec); got.Owner != testOwner {
		t.Fatalf("claimed %+v", got)
	}
	if got := <-sshd.claimed; got != testOwner {
		t.Fatalf("ClaimByEdge(%+v)", got)
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
