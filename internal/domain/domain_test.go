package domain

import (
	"strings"
	"testing"
	"time"
)

func TestTerminalTypes(t *testing.T) {
	started := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	terminal := Terminal{Member: MemberID("member-1"), ContainerID: "container-1", Image: "standard:latest", StartedAt: started}
	if terminal.Member != "member-1" || terminal.ContainerID != "container-1" || terminal.Image != "standard:latest" || !terminal.StartedAt.Equal(started) {
		t.Fatalf("terminal = %+v", terminal)
	}
	status := TerminalStatus{Running: true, Image: terminal.Image, StartedAt: started, Tabs: []string{"main", "t2"}}
	if !status.Running || status.Image != terminal.Image || !status.StartedAt.Equal(started) || len(status.Tabs) != 2 {
		t.Fatalf("status = %+v", status)
	}
}

func TestRunStatusTerminal(t *testing.T) {
	terminal := []RunStatus{RunMerged, RunAbandoned, RunFailed, RunInterrupted}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%q.Terminal() = false, want true", s)
		}
	}
	active := []RunStatus{RunQueued, RunProvisioning, RunRunning, RunNeedsAttention}
	for _, s := range active {
		if s.Terminal() {
			t.Errorf("%q.Terminal() = true, want false", s)
		}
	}
}

func TestRunStatusValid(t *testing.T) {
	all := []RunStatus{
		RunQueued, RunProvisioning, RunRunning, RunNeedsAttention,
		RunMerged, RunAbandoned, RunFailed, RunInterrupted,
	}
	for _, s := range all {
		if !s.Valid() {
			t.Errorf("%q.Valid() = false, want true", s)
		}
	}
	if RunStatus("bogus").Valid() {
		t.Error(`RunStatus("bogus").Valid() = true, want false`)
	}
}

func TestAllRunStatusesComplete(t *testing.T) {
	seen := make(map[RunStatus]bool)
	for _, s := range AllRunStatuses {
		if !s.Valid() {
			t.Errorf("AllRunStatuses contains invalid status %q", s)
		}
		if seen[s] {
			t.Errorf("AllRunStatuses contains %q twice", s)
		}
		seen[s] = true
	}
	if len(AllRunStatuses) != 8 {
		t.Errorf("AllRunStatuses has %d entries, want 8", len(AllRunStatuses))
	}
}

func TestLaunchModeValid(t *testing.T) {
	if !LaunchTUI.Valid() || !LaunchHeadless.Valid() {
		t.Error("defined launch modes must be valid")
	}
	if LaunchMode("bogus").Valid() {
		t.Error(`LaunchMode("bogus").Valid() = true, want false`)
	}
}

func TestRoleValid(t *testing.T) {
	if !RoleViewer.Valid() || !RoleCollaborator.Valid() || !RoleAdmin.Valid() {
		t.Error("defined roles must be valid")
	}
	if Role("bogus").Valid() {
		t.Error(`Role("bogus").Valid() = true, want false`)
	}
}

func TestWorkspaceEnvironmentRetainsVariablesAndSetupPolicy(t *testing.T) {
	env := WorkspaceEnvironment{
		Variables: map[string]string{"AETHER_MODE": "bootstrap"},
		SetupPolicy: SetupPolicy{
			Script: "echo setup",
		},
	}
	w := Workspace{ID: "ws_1", Name: "project", Environment: env}
	if w.Environment.Variables["AETHER_MODE"] != "bootstrap" {
		t.Fatalf("environment variables were not retained: %+v", w.Environment.Variables)
	}
	if w.Environment.SetupPolicy.Script != "echo setup" {
		t.Fatalf("workspace environment was not retained: %+v", w.Environment.SetupPolicy)
	}
}

func TestWorkspaceEnvironmentValidation(t *testing.T) {
	tests := []struct {
		name string
		env  WorkspaceEnvironment
		want bool
	}{
		{"empty", WorkspaceEnvironment{}, true},
		{"variables", WorkspaceEnvironment{Variables: map[string]string{"AETHER_MODE": "bootstrap"}}, true},
		{"invalid variable", WorkspaceEnvironment{Variables: map[string]string{"": "value"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.Valid(); got != tt.want {
				t.Fatalf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A display name is whatever a member typed into the SSH username an
// invite was redeemed with. One holding angle brackets or a line break
// would forge the author address on every commit a run of theirs makes,
// so it never reaches the identity: the member id does instead.
func TestGitIdentityRejectsAnUnusableDisplayName(t *testing.T) {
	tests := []struct {
		name        string
		displayName string
		wantName    string
	}{
		{"plain", "Ada Lovelace", "Ada Lovelace"},
		{"angle brackets", "Eve <attacker@evil.com>", "m_01"},
		{"newline", "Eve\nCo-authored-by: Eve <attacker@evil.com>", "m_01"},
		{"carriage return", "Eve\rmore", "m_01"},
		{"empty", "", "m_01"},
		{"padded", "  Ada  ", "m_01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Member{ID: "m_01", DisplayName: tt.displayName}
			got := m.GitIdentity()
			if got.Name != tt.wantName {
				t.Errorf("GitIdentity().Name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Email != "m_01@aether.local" {
				t.Errorf("GitIdentity().Email = %q, want the fallback address", got.Email)
			}
			if strings.Count(got.Trailer(), "<") != 1 {
				t.Errorf("trailer %q carries more than one address", got.Trailer())
			}
		})
	}
}

// A stored git identity is validated on the way in, and again here: the
// row is not the only way a value can land in those columns.
func TestGitIdentityIgnoresAnUnusableStoredValue(t *testing.T) {
	m := &Member{
		ID:          "m_01",
		DisplayName: "Ada Lovelace",
		GitName:     "Eve <attacker@evil.com>",
		GitEmail:    "not an address",
	}
	got := m.GitIdentity()
	if got.Name != "Ada Lovelace" || got.Email != "m_01@aether.local" {
		t.Fatalf("GitIdentity() = %v, want the display name at the fallback address", got)
	}
}

func TestValidOrigin(t *testing.T) {
	valid := []string{
		"",
		"https://github.com/acme/app.git",
		"http://git.internal/acme/app.git",
		"ssh://git@github.com/acme/app.git",
		"git://git.internal/acme/app.git",
		"/srv/git/app.git",
		"git@github.com:acme/app.git",
		"my-user.name_1@host.example:acme/app.git",
	}
	for _, url := range valid {
		if !ValidOrigin(url) {
			t.Errorf("ValidOrigin(%q) = false, want true", url)
		}
	}
	invalid := []string{
		"github.com/acme/app.git",
		"--upload-pack=/bin/sh",
		"-oProxyCommand=touch /tmp/pwned",
		"https://example.com/a b.git",
		"https://example.com/a.git\nssh://evil",
		"ext::sh -c whoami",
		"file:///srv/git/app.git",
		"git@github.com/acme/app.git",
		"https://example.com/" + strings.Repeat("a", 1024),
	}
	for _, url := range invalid {
		if ValidOrigin(url) {
			t.Errorf("ValidOrigin(%q) = true, want false", url)
		}
	}
}
