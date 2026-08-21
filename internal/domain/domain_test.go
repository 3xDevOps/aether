package domain

import (
	"testing"
	"time"
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

func TestWorkspaceShellModeValid(t *testing.T) {
	if !WorkspaceShellBootstrapTools.Valid() || !WorkspaceShellHarnessLogin.Valid() || !WorkspaceShellAgentSetup.Valid() {
		t.Fatal("defined workspace shell modes must be valid")
	}
	if WorkspaceShellMode("invalid").Valid() {
		t.Fatal("invalid workspace shell mode must be rejected")
	}
}

func TestWorkspaceShellRequestAgentSetup(t *testing.T) {
	valid := WorkspaceShellRequest{
		Workspace: WorkspaceSelector{ID: "ws_1"},
		Mode:      WorkspaceShellAgentSetup,
		Harness:   "omp",
		TUIArgs:   []string{"omp", "{task}"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid agent-setup request rejected: %v", err)
	}
	for name, req := range map[string]WorkspaceShellRequest{
		"missing harness": {
			Workspace: WorkspaceSelector{ID: "ws_1"},
			Mode:      WorkspaceShellAgentSetup,
		},
		"argv proposal outside agent-setup": {
			Workspace: WorkspaceSelector{ID: "ws_1"},
			Mode:      WorkspaceShellHarnessLogin,
			Harness:   "claude",
			TUIArgs:   []string{"claude", "{task}"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := req.Validate(); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestWorkspaceShellRequestValidatesWorkspaceSelector(t *testing.T) {
	base := WorkspaceShellRequest{
		Workspace: WorkspaceSelector{ID: "ws_1"},
		Mode:      WorkspaceShellBootstrapTools,
		Cols:      80,
		Rows:      24,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for name, req := range map[string]WorkspaceShellRequest{
		"missing selector": {Mode: WorkspaceShellBootstrapTools},
		"both selector forms": {
			Workspace: WorkspaceSelector{ID: "ws_1", Name: "project"},
			Mode:      WorkspaceShellBootstrapTools,
		},
		"invalid mode": {
			Workspace: WorkspaceSelector{ID: "ws_1"},
			Mode:      WorkspaceShellMode("invalid"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := req.Validate(); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestToolSnapshotMetadataIsStable(t *testing.T) {
	created := time.Date(2026, 8, 19, 10, 11, 12, 0, time.UTC)
	snapshot := ToolSnapshot{
		ID:          "snapshot_1",
		WorkspaceID: "ws_1",
		MemberID:    "member_1",
		Digest:      "sha256:abc",
		Manifest: ToolManifest{
			Executable: "omp",
			Version:    "1.2.3",
			Metadata:   map[string]string{"source": "bootstrap"},
		},
		CreatedAt: created,
	}
	if snapshot.ID == "" || snapshot.WorkspaceID == "" || snapshot.MemberID == "" ||
		snapshot.Digest == "" || snapshot.Manifest.Executable != "omp" ||
		snapshot.Manifest.Version != "1.2.3" || !snapshot.CreatedAt.Equal(created) {
		t.Fatalf("snapshot metadata was not retained: %+v", snapshot)
	}
}
