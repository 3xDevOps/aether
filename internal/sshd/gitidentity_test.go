package sshd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMemberGit(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = filepath.Join(t.TempDir(), "invites") })
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobC := controlAs(t, e, bobSigner)
	adminC := controlClient(t, e)

	// Setting your own identity: allowed and persisted.
	var res protocol.MemberGitResult
	if err := bobC.Call(protocol.MethodMemberGit, protocol.MemberGitParams{
		Name: "Ada Lovelace", Email: "ada@example.com",
	}, &res); err != nil {
		t.Fatalf("self member.git: %v", err)
	}
	if res.Member.ID != string(bob.ID) || res.Member.GitName != "Ada Lovelace" || res.Member.GitEmail != "ada@example.com" {
		t.Errorf("self member.git = %+v, want id %s Ada Lovelace <ada@example.com>", res.Member, bob.ID)
	}
	got, err := e.store.GetMember(context.Background(), bob.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if got.GitName != "Ada Lovelace" || got.GitEmail != "ada@example.com" {
		t.Errorf("persisted identity = %q %q, want Ada Lovelace ada@example.com", got.GitName, got.GitEmail)
	}
	// A live run's co-author file is refreshed for whoever the identity
	// belongs to.
	wantRefresh := "refresh-coauthors:" + string(bob.ID)
	if calls := e.runs.Calls(); len(calls) != 1 || calls[0] != wantRefresh {
		t.Fatalf("calls after self member.git = %v, want [%s]", calls, wantRefresh)
	}

	// Non-admin setting someone else's identity: denied.
	var pe *protocol.Error
	err = bobC.Call(protocol.MethodMemberGit, protocol.MemberGitParams{
		MemberID: string(e.member.ID), Name: "Grace Hopper", Email: "grace@example.com",
	}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("non-admin set other = %v, want CodeDenied", err)
	}

	// Admin setting another member's identity: allowed.
	if aerr := adminC.Call(protocol.MethodMemberGit, protocol.MemberGitParams{
		MemberID: string(bob.ID), Name: "Grace Hopper", Email: "grace@example.com",
	}, &res); aerr != nil {
		t.Fatalf("admin member.git: %v", aerr)
	}
	if res.Member.GitName != "Grace Hopper" || res.Member.GitEmail != "grace@example.com" {
		t.Errorf("admin set = %+v, want Grace Hopper <grace@example.com>", res.Member)
	}
	// The refresh follows the target, not the admin who made the change,
	// and the denied call above refreshed nobody.
	if calls := e.runs.Calls(); len(calls) != 2 || calls[1] != wantRefresh {
		t.Fatalf("calls after admin member.git = %v, want a second %s", calls, wantRefresh)
	}

	// Invalid input: rejected without touching the stored identity.
	for _, p := range []protocol.MemberGitParams{
		{Name: "Ada <ada>", Email: "ada@example.com"},
		{Name: "Ada", Email: "not-an-address"},
	} {
		err = bobC.Call(protocol.MethodMemberGit, p, nil)
		if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
			t.Fatalf("member.git %+v = %v, want CodeInvalidParams", p, err)
		}
	}

	// Empty values clear both halves back to the fallback. The result
	// decodes into a fresh struct because both fields are omitempty.
	var cleared protocol.MemberGitResult
	if err = bobC.Call(protocol.MethodMemberGit, protocol.MemberGitParams{}, &cleared); err != nil {
		t.Fatalf("clear member.git: %v", err)
	}
	if cleared.Member.GitName != "" || cleared.Member.GitEmail != "" {
		t.Errorf("cleared identity = %+v, want both empty", cleared.Member)
	}
	got, err = e.store.GetMember(context.Background(), bob.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if id := got.GitIdentity(); id.Name != "Bob" || id.Email != string(bob.ID)+"@aether.local" {
		t.Errorf("fallback identity = %v, want Bob <%s@aether.local>", id, bob.ID)
	}
}
