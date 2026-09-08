package scheduler

import (
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// countTimelineEvents drains what the subscription has already queued and
// counts the timeline entries of one kind among it.
func countTimelineEvents(sub events.Subscription, kind events.TimelineKind) int {
	n := 0
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				return n
			}
			if p, isTL := ev.Payload.(events.TimelinePayload); isTL && p.Kind == kind {
				n++
			}
		default:
			return n
		}
	}
}

// newSteerer creates a second collaborator who can steer e.member's runs.
func newSteerer(t *testing.T, e *testEnv, name, gitName, gitEmail string) *domain.Member {
	t.Helper()
	m := &domain.Member{
		DisplayName: name, PublicKey: testPublicKey(t),
		Color: "#3cb44b", Role: domain.RoleCollaborator,
		GitName: gitName, GitEmail: gitEmail,
	}
	if err := e.db.CreateMember(t.Context(), m); err != nil {
		t.Fatalf("create member %s: %v", name, err)
	}
	return m
}

// TestLaunchSpecUsesMemberGitIdentity pins that the container's git
// identity is the one the member set, not their display name and internal
// address.
func TestLaunchSpecUsesMemberGitIdentity(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := e.db.UpdateMemberGitIdentity(t.Context(), e.member.ID, "Ada Lovelace", "ada@example.com"); err != nil {
		t.Fatalf("UpdateMemberGitIdentity: %v", err)
	}
	_, c := e.launchFake(t, "identity check")

	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		if c.spec.Env[key] != "Ada Lovelace" {
			t.Errorf("%s = %q, want Ada Lovelace", key, c.spec.Env[key])
		}
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		if c.spec.Env[key] != "ada@example.com" {
			t.Errorf("%s = %q, want ada@example.com", key, c.spec.Env[key])
		}
	}
}

// TestSteeringByTypingIsRecordedOnce covers the half that has no RPC of
// its own: someone other than the owner typing into a run's terminal makes
// them a co-author, stamped on the timeline exactly once however often
// they type.
func TestSteeringByTypingIsRecordedOnce(t *testing.T) {
	e := newTestEnv(t, nil)
	bob := newSteerer(t, e, "Bob", "Bob Steer", "bob@example.com")
	run, _ := e.launchFake(t, "add OAuth login")
	sub := e.subscribe(t)

	e.sched.RecordSteer(t.Context(), run.ID, bob.ID)
	e.sched.RecordSteer(t.Context(), run.ID, bob.ID)
	// The owner typing into their own run credits nobody.
	e.sched.RecordSteer(t.Context(), run.ID, e.member.ID)

	steerers, err := e.db.ListRunSteerers(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("ListRunSteerers: %v", err)
	}
	if len(steerers) != 1 || steerers[0].ID != bob.ID {
		t.Fatalf("steerers = %+v, want only %s", steerers, bob.ID)
	}
	ev := waitTimelineEvent(t, sub, run.ID, events.TimelineCoAuthor)
	if ev.ActorID != bob.ID {
		t.Errorf("co-author event actor = %s, want %s", ev.ActorID, bob.ID)
	}
	if got := countTimelineEvents(sub, events.TimelineCoAuthor); got != 0 {
		t.Errorf("co-author events after the first = %d, want 0", got)
	}
}

// TestAetherCommitCreditsOwnerAndSteerers is the commit half: Aether's own
// commit is authored as the run owner and carries one trailer per steerer,
// with the fallback address for a member who set no git email.
func TestAetherCommitCreditsOwnerAndSteerers(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := e.db.UpdateMemberGitIdentity(t.Context(), e.member.ID, "Ada Lovelace", "ada@example.com"); err != nil {
		t.Fatalf("UpdateMemberGitIdentity: %v", err)
	}
	bob := newSteerer(t, e, "Bob", "Bob Steer", "bob@example.com")
	carol := newSteerer(t, e, "Carol", "", "")
	run, _ := e.launchFake(t, "add OAuth login")
	e.sched.RecordSteer(t.Context(), run.ID, bob.ID)
	e.sched.RecordSteer(t.Context(), run.ID, carol.ID)

	if _, err := e.sched.commitAll(t.Context(), run.ID, "wip: add OAuth login"); err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	messages := e.git.commitsFor(run.ID)
	if len(messages) != 1 {
		t.Fatalf("commits = %v, want one", messages)
	}
	for _, want := range []string{
		"Co-authored-by: Bob Steer <bob@example.com>",
		"Co-authored-by: Carol <" + string(carol.ID) + "@aether.local>",
	} {
		if !strings.Contains(messages[0], want) {
			t.Errorf("commit message %q missing %q", messages[0], want)
		}
	}
	authors := e.git.commitAuthors(run.ID)
	if len(authors) != 1 || authors[0].String() != "Ada Lovelace <ada@example.com>" {
		t.Errorf("commit author = %v, want the run owner", authors)
	}

	// A handoff makes a steerer the owner: they become the author, and
	// crediting them as their own co-author would be noise.
	if err := e.db.TransferRun(t.Context(), run.ID, bob.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	if _, err := e.sched.commitAll(t.Context(), run.ID, "aether: add OAuth login"); err != nil {
		t.Fatalf("commitAll after handoff: %v", err)
	}
	messages = e.git.commitsFor(run.ID)
	if strings.Contains(messages[1], "Bob Steer") {
		t.Errorf("commit after handoff credits its own author: %q", messages[1])
	}
	if authors = e.git.commitAuthors(run.ID); authors[1].String() != "Bob Steer <bob@example.com>" {
		t.Errorf("commit author after handoff = %v, want the new owner", authors[1])
	}
}

// TestRunCoAuthorsFileTracksSteerers pins the container's side of the
// contract: the list is in the coordination directory before the container
// starts, and it is rewritten as steerers join.
func TestRunCoAuthorsFileTracksSteerers(t *testing.T) {
	staged := fakeServerBinary(t, "#!/bin/sh\necho aether\n")
	e := newTestEnv(t, withServerBinary(staged))
	coord, _ := withCoordination(t, e)
	bob := newSteerer(t, e, "Bob", "Bob Steer", "bob@example.com")

	run, container := e.launchFake(t, "add OAuth login")
	if got := coord.trailers(run.ID); len(got) != 0 {
		t.Fatalf("co-authors at launch = %v, want empty", got)
	}
	if !strings.Contains(container.spec.Command[len(container.spec.Command)-1], coAuthorsPath) {
		t.Errorf("task prompt %q does not name %s", container.spec.Command, coAuthorsPath)
	}

	e.sched.RecordSteer(t.Context(), run.ID, bob.ID)
	want := []string{"Co-authored-by: Bob Steer <bob@example.com>"}
	if got := coord.trailers(run.ID); !slices.Equal(got, want) {
		t.Errorf("co-authors after steering = %v, want %v", got, want)
	}
}

// A handoff swaps the author and one of the co-authors. The commit path
// recomputes per commit, but the file the agent reads does not - left
// alone it would have the new owner instruct their own agent to credit
// them as their own co-author, and would drop the outgoing owner, who did
// the work, from the branch entirely.
//
// The container's own author does not move with the run: it was frozen
// when the container was created. So after the handoff the agent is still
// committing as the outgoing owner, and the file must not tell it to
// credit that address - while Aether's own commit, authored as the new
// owner, must.
func TestHandoffRewritesTheCoAuthorList(t *testing.T) {
	staged := fakeServerBinary(t, "#!/bin/sh\necho aether\n")
	e := newTestEnv(t, withServerBinary(staged))
	coord, _ := withCoordination(t, e)
	if err := e.db.UpdateMemberGitIdentity(t.Context(), e.member.ID, "Ada Lovelace", "ada@example.com"); err != nil {
		t.Fatalf("UpdateMemberGitIdentity: %v", err)
	}
	bob := newSteerer(t, e, "Bob", "Bob Steer", "bob@example.com")

	run, _ := e.launchFake(t, "add OAuth login")
	e.sched.RecordSteer(t.Context(), run.ID, bob.ID)
	if got := coord.trailers(run.ID); !slices.Equal(got, []string{"Co-authored-by: Bob Steer <bob@example.com>"}) {
		t.Fatalf("co-authors before the handoff = %v", got)
	}

	if err := e.db.TransferRun(t.Context(), run.ID, bob.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	e.sched.RecordHandoff(t.Context(), run.ID, e.member.ID)

	// Bob owns the run and the agent still commits as Ada, so there is
	// nobody left for the agent to credit.
	if got := coord.trailers(run.ID); len(got) != 0 {
		t.Errorf("co-authors after the handoff = %v, want none: the agent commits as Ada already", got)
	}
	// Aether's own commit is authored as Bob, so it does credit Ada.
	if _, err := e.sched.commitAll(t.Context(), run.ID, "aether: add OAuth login"); err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	message := e.git.commitsFor(run.ID)[0]
	if !strings.Contains(message, "Co-authored-by: Ada Lovelace <ada@example.com>") {
		t.Errorf("commit after the handoff = %q, want the outgoing owner credited", message)
	}
	if strings.Contains(message, "Bob Steer") {
		t.Errorf("commit after the handoff = %q, credits its own author", message)
	}
}

// Two members can stand behind one address - a shared account, or one
// person joined twice. Git and GitHub credit the address, so a second
// trailer says nothing, and one matching the address the commit is
// already authored as credits the author twice.
func TestCoAuthorTrailersDedupeByAddress(t *testing.T) {
	e := newTestEnv(t, nil)
	first := newSteerer(t, e, "Bot", "Release Bot", "bot@example.com")
	second := newSteerer(t, e, "Bot on the laptop", "Release Bot", "BOT@example.com")
	run, _ := e.launchFake(t, "add OAuth login")
	e.sched.RecordSteer(t.Context(), run.ID, first.ID)
	e.sched.RecordSteer(t.Context(), run.ID, second.ID)

	// Which of the two rows wins is the store's ordering to decide; that
	// exactly one line comes out for the address is not.
	trailers, err := e.sched.runCoAuthors(t.Context(), run)
	if err != nil {
		t.Fatalf("runCoAuthors: %v", err)
	}
	if len(trailers) != 1 || !strings.EqualFold(trailers[0], "Co-authored-by: Release Bot <bot@example.com>") {
		t.Errorf("trailers = %v, want one line for the shared address", trailers)
	}

	// Seeded with the address the commit is authored as, neither row is
	// credited: skipping the owner by member id alone would miss this.
	seeded, err := e.sched.runCoAuthors(t.Context(), run, "Bot@Example.com")
	if err != nil {
		t.Fatalf("runCoAuthors: %v", err)
	}
	if len(seeded) != 0 {
		t.Errorf("trailers = %v, want none for the address that already authored the commit", seeded)
	}
}

// A member who sets their git identity while a run is live has that run's
// list rewritten, so the agent's next commit credits the address they just
// gave rather than the aether.local fallback.
func TestGitIdentityChangeRefreshesLiveRuns(t *testing.T) {
	staged := fakeServerBinary(t, "#!/bin/sh\necho aether\n")
	e := newTestEnv(t, withServerBinary(staged))
	coord, _ := withCoordination(t, e)
	bob := newSteerer(t, e, "Bob", "", "")

	run, _ := e.launchFake(t, "add OAuth login")
	e.sched.RecordSteer(t.Context(), run.ID, bob.ID)
	fallback := "Co-authored-by: Bob <" + string(bob.ID) + "@aether.local>"
	if got := coord.trailers(run.ID); !slices.Equal(got, []string{fallback}) {
		t.Fatalf("co-authors before the change = %v, want %q", got, fallback)
	}

	if err := e.db.UpdateMemberGitIdentity(t.Context(), bob.ID, "Bob Steer", "bob@example.com"); err != nil {
		t.Fatalf("UpdateMemberGitIdentity: %v", err)
	}
	e.sched.RefreshMemberCoAuthors(t.Context(), bob.ID)
	want := []string{"Co-authored-by: Bob Steer <bob@example.com>"}
	if got := coord.trailers(run.ID); !slices.Equal(got, want) {
		t.Errorf("co-authors after the change = %v, want %v", got, want)
	}

	// A member the run does not credit leaves it alone.
	before := coord.writes(run.ID)
	e.sched.RefreshMemberCoAuthors(t.Context(), e.member.ID)
	if got := coord.writes(run.ID); got != before {
		t.Errorf("an unrelated identity change rewrote the list %d times", got-before)
	}
}
