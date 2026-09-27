package edgetest

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// TestFailedClaimRecordRecovers makes the edge fail to record the owner
// after the server made the claimant its admin and used up the code. The
// edge disconnects the server on its own, the server gives up its belief
// that it is claimed when the edge reports it unclaimed, a fresh code then
// claims it again for the same account without changing its members, and
// no other account can use the gap.
func TestFailedClaimRecordRecovers(t *testing.T) {
	h := newHarness(t)
	a := h.newServer()
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	ctx := context.Background()

	// The fault is injected into the edge's own database, so nothing in
	// the edge's code path changes.
	edgeDB, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.Join(h.edgeDir, "edge.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edgeDB.Close() })
	if _, err = edgeDB.Exec(`CREATE TRIGGER fail_claim BEFORE INSERT ON servers
		BEGIN SELECT RAISE(ABORT, 'injected: the edge cannot record the owner'); END`); err != nil {
		t.Fatal(err)
	}
	from := h.proxy.mark()
	_, err = h.claim(al, a.claimCode(t, time.Now()))
	if err == nil || !strings.Contains(err.Error(), "claim it again") {
		t.Fatalf("claim the edge could not record: %v; want the unsettled claim and what to do next", err)
	}
	members, err := a.db.ListMembers(ctx)
	if err != nil || len(members) != 1 || members[0].Role != domain.RoleAdmin {
		t.Fatalf("members after the half-done claim = %+v, %v; want alice as admin", members, err)
	}
	admin := members[0].ID
	if owner, ownerErr := a.state(t).Owner(); ownerErr != nil || owner == nil {
		t.Fatalf("server's owner after the half-done claim = %+v, %v; want alice", owner, ownerErr)
	}
	if _, err = edgeDB.Exec(`DROP TRIGGER fail_claim`); err != nil {
		t.Fatal(err)
	}

	// The edge closed the control channel; the server's next enrollment
	// is told it is unclaimed.
	h.proxy.await(t, "the server back, unclaimed", from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.Ready)
		return ok && r.ServerID == a.id && r.State == edgeproto.StateUnclaimed
	})
	eventually(t, "the server forgets the owner the edge never recorded", func() error {
		owner, ownerErr := a.state(t).Owner()
		if ownerErr == nil && owner != nil {
			ownerErr = errors.New("still claimed by " + owner.Login)
		}
		return ownerErr
	})

	// As `aether-server edge claim-code` does, now that the server is
	// unclaimed.
	code := a.claimCode(t, time.Now())
	_, err = h.claim(bo, code)
	wantRefusal(t, "another account claiming the unrecorded server", err, edgeproto.RefusalClaimed)
	res, err := h.claim(al, code)
	if err != nil || res.ServerID != a.id {
		t.Fatalf("alice claims again: %+v, %v", res, err)
	}
	members, err = a.db.ListMembers(ctx)
	if err != nil || len(members) != 1 || members[0].ID != admin || members[0].Role != domain.RoleAdmin {
		t.Fatalf("members after the second claim = %+v, %v; want alice unchanged", members, err)
	}
	h.mustDial(al, a)
	if _, err = h.dial(bo, h.link(a)); err == nil {
		t.Fatal("bob reached the server")
	}
}
