package edgeagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

var testOwner = edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo"}

// pinnedState is the state under a new data directory with a new edge key
// pinned, as it is once the server has enrolled.
func pinnedState(t *testing.T) *State {
	t.Helper()
	s := OpenState(t.TempDir())
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(key); err != nil {
		t.Fatal(err)
	}
	return s
}

func issue(t *testing.T, s *State, at time.Time) string {
	t.Helper()
	return issueAdmin(t, s, "", at)
}

func issueAdmin(t *testing.T, s *State, admin string, at time.Time) string {
	t.Helper()
	code, _, err := s.IssueClaimCode(edgeproto.ServerID(newHostKey(t).PublicKey()), admin, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func claimOK(string) error { return nil }

func wrongCode(code string) string {
	last := "a"
	if strings.HasSuffix(code, "a") {
		last = "b"
	}
	return code[:len(code)-1] + last
}

func TestClaimCodeAllowsFiveAttempts(t *testing.T) {
	s := pinnedState(t)
	now := time.Now()
	code := issue(t, s, now)
	claims := 0
	claim := func(string) error { claims++; return nil }
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
	s := pinnedState(t)
	issued := time.Now()
	code := issue(t, s, issued)
	late := issued.Add(edgeproto.ClaimCodeTTL + time.Second)
	if err := s.attemptClaim(code, testOwner, late, claimOK); !errors.Is(err, edgeproto.RefusalClaimExpired) {
		t.Fatalf("expired code: %v", err)
	}
	if err := s.attemptClaim(code, testOwner, issued, claimOK); err == nil {
		t.Fatal("an expired code claimed after the clock went back")
	}
}

func TestClaimCodeIsUsedOnce(t *testing.T) {
	s := pinnedState(t)
	now := time.Now()
	code := issueAdmin(t, s, "m-1", now)
	raw, err := os.ReadFile(s.path(claimFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), code[edgeproto.ServerIDLength+1:]) {
		t.Fatal("the claim code's secret is stored in the clear")
	}
	var admin string
	if err := s.attemptClaim("  "+strings.ToUpper(code)+"\n", testOwner, now, func(a string) error { admin = a; return nil }); err != nil {
		t.Fatalf("right code as a person typed it: %v", err)
	}
	if admin != "m-1" {
		t.Fatalf("the claim ran for admin %q, want the member the code names", admin)
	}
	if owner, _ := s.Owner(); owner == nil || *owner != testOwner {
		t.Fatalf("owner = %+v", owner)
	}
	if _, ok, _ := s.ClaimCode(); ok {
		t.Error("a used code was kept")
	}
	if err := s.attemptClaim(code, testOwner, now, claimOK); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("second claim: %v, want already claimed", err)
	}
}

func TestFailedClaimKeepsTheCodeButSpendsTheAttempt(t *testing.T) {
	s := pinnedState(t)
	now := time.Now()
	code := issue(t, s, now)
	boom := errors.New("store is read-only")
	if err := s.attemptClaim(code, testOwner, now, func(string) error { return boom }); !errors.Is(err, boom) {
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
	if err := c.attempt(wrongCode(code), func(string) error { t.Fatal("a wrong code ran the claim"); return nil }); !errors.Is(err, edgeproto.RefusalClaimWrong) {
		t.Fatalf("wrong code: %v", err)
	}
	claimed := false
	if err := c.attempt(code, func(string) error { claimed = true; return nil }); err != nil || !claimed {
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

// A claim outlives the control connection that opened it: the owner is
// reported on the connection that replaced it.
func TestClaimIsReportedOnTheLiveControlConnection(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	code := issueFor(t, a)
	connID := edgeproto.NewConnID()
	edge.openKind(ec, edgeproto.KindClaim, connID, edge.grant(t, claimGrant(t, a, connID)))
	expect[edgeproto.OpenResult](t, ec)
	data := edge.nextData(t)
	t.Cleanup(func() { _ = data.CloseNow() })
	c := nextClaim(t, sshd)
	_ = ec.c.CloseNow()
	next := edge.nextControl(t)
	expect[edgeproto.Directory](t, next)
	if err := c.attempt(code, claimOK); err != nil {
		t.Fatalf("claim after the control connection was replaced: %v", err)
	}
	if got := expect[edgeproto.Claimed](t, next); got.ConnID != connID || got.Owner != edgeproto.AccountPrincipal(testOwner) {
		t.Fatalf("claimed %+v on the live connection", got)
	}
	if owner, _ := a.state.Owner(); owner == nil || *owner != testOwner {
		t.Fatalf("owner = %+v", owner)
	}
}

// A claim the edge cannot be told about records no owner and keeps the
// code for another attempt.
func TestClaimWithoutAControlConnectionRecordsNothing(t *testing.T) {
	edge := newFakeEdge(t)
	a := newAgent(t, edge.srv.URL, t.TempDir(), newFakeSSH())
	if err := a.state.Pin(edge.pub); err != nil {
		t.Fatal(err)
	}
	code := issueFor(t, a)
	err := a.claimAttempt(claimGrant(t, a, edgeproto.NewConnID()))(code, claimOK)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("claim while not connected = %v", err)
	}
	if owner, _ := a.state.Owner(); owner != nil {
		t.Fatalf("an unreported claim recorded owner %+v", owner)
	}
	if c, ok, _ := a.state.ClaimCode(); !ok || !c.Usable(time.Now()) {
		t.Fatalf("claim code after an unreported claim: %+v, %v", c, ok)
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

// An account deletion reaches sshd, and the edge hears it was applied.
func TestAccountDeletedReachesSSH(t *testing.T) {
	_, sshd, _, ec := enrolled(t)
	deleted := edgeproto.AccountDeleted{Provider: testOwner.Provider, Subject: testOwner.Subject}
	ec.send(deleted)
	select {
	case got := <-sshd.deleted:
		if got != deleted {
			t.Fatalf("EdgeAccountDeleted(%+v)", got)
		}
	case <-time.After(waitFor):
		t.Fatal("sshd was not told about the deleted account")
	}
	if got := expect[edgeproto.AccountDeletionApplied](t, ec); edgeproto.AccountDeleted(got) != deleted {
		t.Fatalf("applied %+v, want %+v", got, deleted)
	}
}

// A deletion the server failed to apply is not answered, and the control
// connection ends, so the edge sends it again at the next enrollment.
func TestAccountDeletionNotAppliedIsNotAnswered(t *testing.T) {
	edge, sshd, _, ec := enrolled(t)
	sshd.mu.Lock()
	sshd.deleteErr = errors.New("database is locked")
	sshd.mu.Unlock()
	ec.send(edgeproto.AccountDeleted{Provider: testOwner.Provider, Subject: testOwner.Subject})
	<-sshd.deleted
	for deadline := time.After(waitFor); ; {
		select {
		case m, open := <-ec.msgs:
			if applied, ok := m.(edgeproto.AccountDeletionApplied); ok {
				t.Fatalf("a failed deletion was answered %+v", applied)
			}
			if open {
				continue
			}
		case <-deadline:
			t.Fatal("the control connection stayed open after a deletion failed")
		}
		break
	}
	sshd.mu.Lock()
	sshd.deleteErr = nil
	sshd.mu.Unlock()
	next := edge.nextControl(t)
	next.send(edgeproto.AccountDeleted{Provider: testOwner.Provider, Subject: testOwner.Subject})
	<-sshd.deleted
	expect[edgeproto.AccountDeletionApplied](t, next)
}

func TestTransferOwner(t *testing.T) {
	edge := newFakeEdge(t)
	dir := t.TempDir()
	sshd := newFakeSSH()
	next := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2002", Login: "next", Email: "next@example.com"}
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
	if err := a.state.writeOwner(testOwner); err != nil {
		t.Fatal(err)
	}
	// The transfer stands only once the edge answers that it recorded it.
	transfer := func(answer string) error {
		done := make(chan error, 1)
		go func() { done <- a.TransferOwner(next) }()
		got := expect[edgeproto.OwnerTransferred](t, ec)
		if got.Owner != edgeproto.AccountPrincipal(next) {
			t.Fatalf("owner_transferred %+v", got)
		}
		ec.send(edgeproto.OwnerTransferResult{ID: got.ID, Owner: got.Owner, Error: answer})
		return <-done
	}
	refusal := "ownership report refused: account is blocked by this edge's operator"
	if err := transfer(refusal); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("transfer the edge refused = %v", err)
	}
	if owner, _ := a.state.Owner(); owner == nil || *owner != testOwner {
		t.Fatalf("owner after a refused transfer = %+v, want the previous one", owner)
	}
	if err := transfer(""); err != nil {
		t.Fatal(err)
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
	if err := a.state.Pin(edge.pub); err != nil {
		t.Fatal(err)
	}
	if err := a.state.writeOwner(testOwner); err != nil {
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

// An owner v0.5.2-alpha.3 recorded through Google is forgotten at
// enrollment: the edge is told the server is ownerless, and the control
// channel stays up.
func TestGoogleOwnerFromAnEarlierVersion(t *testing.T) {
	edge := newFakeEdge(t)
	edge.state = edgeproto.StateClaimed
	sshd := newFakeSSH()
	google := edgeproto.Account{Provider: "google", Subject: "g-1", Email: "owner@example.com"}
	a := newAgent(t, edge.srv.URL, t.TempDir(), sshd)
	if err := a.state.Pin(edge.pub); err != nil {
		t.Fatal(err)
	}
	if err := a.state.writeOwner(google); err != nil {
		t.Fatal(err)
	}
	run(t, a)
	ec := edge.nextControl(t)
	expect[edgeproto.Ownerless](t, ec)
	if owner, err := a.state.Owner(); owner != nil || err != nil {
		t.Fatalf("owner = %+v, %v; want none", owner, err)
	}
	ec.send(edgeproto.Ping{})
	expect[edgeproto.Pong](t, ec)
}

// A transfer whose answer never came may or may not stand at the edge, so
// every enrollment with a claimed server reports the server's owner again.
func TestOwnerIsReportedAgainAtEnrollment(t *testing.T) {
	edge := newFakeEdge(t)
	edge.state = edgeproto.StateClaimed
	sshd := newFakeSSH()
	sshd.entries = []edgeproto.DirectoryEntry{{Kind: edgeproto.EntryMember, Provider: testOwner.Provider, Subject: testOwner.Subject, Role: "admin"}}
	a := newAgent(t, edge.srv.URL, t.TempDir(), sshd)
	if err := a.state.Pin(edge.pub); err != nil {
		t.Fatal(err)
	}
	if err := a.state.writeOwner(testOwner); err != nil {
		t.Fatal(err)
	}
	run(t, a)
	ec := edge.nextControl(t)
	got := expect[edgeproto.OwnerTransferred](t, ec)
	if got.Owner != edgeproto.AccountPrincipal(testOwner) {
		t.Fatalf("owner reported at enrollment = %+v, want %+v", got.Owner, testOwner)
	}
	ec.send(edgeproto.OwnerTransferResult{ID: got.ID, Owner: got.Owner})
	ec.send(edgeproto.Ping{})
	expect[edgeproto.Pong](t, ec)
	if owner, _ := a.state.Owner(); owner == nil || *owner != testOwner {
		t.Fatalf("owner after the edge's answer = %+v", owner)
	}
}

// A transfer takes only the edge's answer to its own report: not the
// answer to the owner reported at enrollment, arriving before or after
// its own, and not a repeated answer.
func TestTransferTakesOnlyItsOwnAnswer(t *testing.T) {
	edge := newFakeEdge(t)
	edge.state = edgeproto.StateClaimed
	sshd := newFakeSSH()
	next := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2002", Login: "next", Email: "next@example.com"}
	sshd.entries = []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: testOwner.Provider, Subject: testOwner.Subject, Role: "admin"},
		{Kind: edgeproto.EntryMember, Provider: next.Provider, Subject: next.Subject, Role: "admin"},
	}
	a := newAgent(t, edge.srv.URL, t.TempDir(), sshd)
	if err := a.state.Pin(edge.pub); err != nil {
		t.Fatal(err)
	}
	if err := a.state.writeOwner(testOwner); err != nil {
		t.Fatal(err)
	}
	run(t, a)
	ec := edge.nextControl(t)
	report := expect[edgeproto.OwnerTransferred](t, ec)
	reportAnswer := edgeproto.OwnerTransferResult{ID: report.ID, Owner: report.Owner}
	ec.send(edgeproto.Ping{})
	expect[edgeproto.Pong](t, ec)

	const refusal = "ownership report refused: account is blocked by this edge's operator"
	transfer := func(to edgeproto.Account, answers func(got edgeproto.OwnerTransferred) []edgeproto.OwnerTransferResult) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- a.TransferOwner(to) }()
		got := expect[edgeproto.OwnerTransferred](t, ec)
		if got.ID == report.ID {
			t.Fatalf("transfer reported with the enrollment report's id %s", got.ID)
		}
		for _, r := range answers(got) {
			ec.send(r)
		}
		select {
		case err := <-done:
			return err
		case <-time.After(waitFor):
			t.Fatal("transfer took no answer")
			return nil
		}
	}
	wantOwner := func(want edgeproto.Account) {
		t.Helper()
		if owner, err := a.state.Owner(); err != nil || owner == nil || *owner != want {
			t.Fatalf("owner = %+v, %v; want %+v", owner, err, want)
		}
	}

	// The enrollment report's answer arrives first; the transfer's own
	// answer follows it.
	if err := transfer(next, func(got edgeproto.OwnerTransferred) []edgeproto.OwnerTransferResult {
		return []edgeproto.OwnerTransferResult{reportAnswer, {ID: got.ID, Owner: got.Owner}}
	}); err != nil {
		t.Fatalf("transfer answered after the enrollment report: %v", err)
	}
	wantOwner(next)
	// Back to the previous owner, whom the edge refuses. The enrollment
	// report's answer for that owner arrives again, before the refusal.
	if err := transfer(testOwner, func(got edgeproto.OwnerTransferred) []edgeproto.OwnerTransferResult {
		return []edgeproto.OwnerTransferResult{reportAnswer, {ID: got.ID, Owner: got.Owner, Error: refusal}}
	}); err == nil || !strings.HasSuffix(err.Error(), ": "+refusal) {
		t.Fatalf("refused transfer after the enrollment report's answer = %v, want %q", err, refusal)
	}
	wantOwner(next)
	// The refusal first, then a repeated answer and the enrollment
	// report's.
	if err := transfer(testOwner, func(got edgeproto.OwnerTransferred) []edgeproto.OwnerTransferResult {
		return []edgeproto.OwnerTransferResult{{ID: got.ID, Owner: got.Owner, Error: refusal}, {ID: got.ID, Owner: got.Owner}, reportAnswer}
	}); err == nil || !strings.HasSuffix(err.Error(), ": "+refusal) {
		t.Fatalf("refused transfer answered again = %v, want %q", err, refusal)
	}
	ec.send(edgeproto.Ping{})
	expect[edgeproto.Pong](t, ec)
	wantOwner(next)
	// A later transfer still takes its own answer.
	if err := transfer(testOwner, func(got edgeproto.OwnerTransferred) []edgeproto.OwnerTransferResult {
		return []edgeproto.OwnerTransferResult{{ID: got.ID, Owner: got.Owner}}
	}); err != nil {
		t.Fatal(err)
	}
	wantOwner(testOwner)
}

func issueFor(t *testing.T, a *Agent) string {
	t.Helper()
	code, _, err := a.state.IssueClaimCode(a.ServerID(), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// A root-run edge command leaves the state to the data directory's
// owner, so a server running as that user still reads and locks it.
func TestStateWrittenAsRootBelongsToTheDataDirOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to write as another user")
	}
	const uid, gid = 4242, 4243
	dataDir := t.TempDir()
	if err := os.Chown(dataDir, uid, gid); err != nil {
		t.Fatal(err)
	}
	s := OpenState(dataDir)
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Pin(key); err != nil {
		t.Fatal(err)
	}
	if err = s.writeOwner(testOwner); err != nil {
		t.Fatal(err)
	}
	issue(t, s, time.Now())
	err = filepath.WalkDir(s.dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st := info.Sys().(*syscall.Stat_t); st.Uid != uid || st.Gid != gid {
			t.Errorf("%s belongs to %d:%d, want %d:%d", path, st.Uid, st.Gid, uid, gid)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
