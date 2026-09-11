package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/store"
)

func storeMemberDefinition(t *testing.T, e *testEnv, member domain.MemberID, def harness.Definition) {
	t.Helper()
	blob, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	row := &store.HarnessDefinition{MemberID: member, Name: def.Name, Definition: blob}
	if err := e.db.UpsertHarnessDefinition(t.Context(), row); err != nil {
		t.Fatalf("upsert definition: %v", err)
	}
}

// A member's stored definition resolves for its owner and only its owner:
// definitions shape argv inside the member's own container, so they are
// member-scoped, never global.
func TestMemberHarnessDefinitionResolution(t *testing.T) {
	e := newTestEnv(t, nil)
	storeMemberDefinition(t, e, e.member.ID, harness.Definition{
		Name:            "aider",
		TUIArgs:         []string{"aider", "{task}"},
		HeadlessArgs:    []string{"aider", "-p", "{task}"},
		Executable:      "aider",
		ProfileRoot:     "/root/.aider",
		CredentialPaths: []string{"/root/.aider"},
	})

	argv, prof, err := e.sched.command(t.Context(), e.member.ID, "aider", domain.LaunchHeadless, "go")
	if err != nil {
		t.Fatalf("member definition did not resolve: %v", err)
	}
	if got, want := fmt.Sprint(argv), fmt.Sprint([]string{"aider", "-p", "go"}); got != want {
		t.Fatalf("argv = %s, want %s", got, want)
	}
	if prof.LocalRoot != "/root/.aider" {
		t.Fatalf("profile root = %q, want /root/.aider", prof.LocalRoot)
	}

	other := &domain.Member{DisplayName: "Eve", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(t.Context(), other); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if _, _, err := e.sched.command(t.Context(), other.ID, "aider", domain.LaunchHeadless, "go"); err == nil {
		t.Fatal("another member resolved a definition they do not own")
	}
}

// The server-wide admin spec pins a name for everyone; a member definition
// must not override it.
func TestServerSpecWinsOverMemberDefinition(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{
			"aider": {
				TUIArgs: []string{"aider", "{task}"}, HeadlessArgs: []string{"aider", "--admin", "{task}"},
				Executable: "aider", ProfileRoot: "/root/.aider", CredentialPaths: []string{"/root/.aider"},
			},
		}
	})
	storeMemberDefinition(t, e, e.member.ID, harness.Definition{
		Name: "aider", TUIArgs: []string{"aider", "{task}"}, HeadlessArgs: []string{"aider", "--member", "{task}"},
		Executable: "aider",
	})
	argv, _, err := e.sched.command(t.Context(), e.member.ID, "aider", domain.LaunchHeadless, "x")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if fmt.Sprint(argv) != fmt.Sprint([]string{"aider", "--admin", "x"}) {
		t.Fatalf("argv = %v, want the admin spec's argv", argv)
	}
}

// A stored blob that fails validation (schema drift, hand-edited row) must
// fail the launch loudly, never resolve to a half-usable profile.
func TestCorruptMemberDefinitionFails(t *testing.T) {
	e := newTestEnv(t, nil)
	row := &store.HarnessDefinition{MemberID: e.member.ID, Name: "aider", Definition: []byte(`{"Name":"aider"}`)}
	if err := e.db.UpsertHarnessDefinition(t.Context(), row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := e.sched.command(t.Context(), e.member.ID, "aider", domain.LaunchHeadless, "x"); err == nil {
		t.Fatal("invalid stored definition accepted")
	}
}

func TestUnknownHarnessErrorNamesAgentAdd(t *testing.T) {
	e := newTestEnv(t, nil)
	_, _, err := e.sched.command(t.Context(), e.member.ID, "nope", domain.LaunchTUI, "x")
	if err == nil {
		t.Fatal("unknown harness accepted")
	}
	if !strings.Contains(err.Error(), "aether agent add nope") {
		t.Fatalf("error %q does not name the onboarding command", err)
	}
}
