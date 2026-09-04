package domain

import (
	"testing"
)

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

func TestWorkspaceEnvironmentRepresentsMigrationFields(t *testing.T) {
	env := WorkspaceEnvironment{
		CustomImage:  "registry.example/workspace:latest",
		NeutralImage: true,
		Variables:    map[string]string{"AETHER_MODE": "bootstrap"},
		SetupPolicy: SetupPolicy{
			Script: "echo setup",
		},
	}
	w := Workspace{ID: "ws_1", Name: "project", Environment: env}
	if w.Environment.CustomImage != env.CustomImage || !w.Environment.NeutralImage {
		t.Fatalf("workspace environment was not retained: %+v", w.Environment)
	}
	if w.Environment.Variables["AETHER_MODE"] != "bootstrap" {
		t.Fatalf("environment variables were not retained: %+v", w.Environment.Variables)
	}
	if w.Environment.SetupPolicy.Script != "echo setup" {
		t.Fatalf("setup policy was not retained: %+v", w.Environment.SetupPolicy)
	}
}

func TestWorkspaceEnvironmentValidation(t *testing.T) {
	tests := []struct {
		name string
		env  WorkspaceEnvironment
		want bool
	}{
		{"neutral", WorkspaceEnvironment{NeutralImage: true}, true},
		{"custom", WorkspaceEnvironment{CustomImage: "image"}, true},
		{"neither image", WorkspaceEnvironment{}, false},
		{"invalid variable", WorkspaceEnvironment{NeutralImage: true, Variables: map[string]string{"": "value"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.Valid(); got != tt.want {
				t.Fatalf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}
