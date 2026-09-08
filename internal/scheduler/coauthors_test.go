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
