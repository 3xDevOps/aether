package runtime

import (
	"strings"
	"testing"
)

func validSpec() Spec {
	return Spec{
		Name:              "test",
		Image:             "busybox:1.36",
		Env:               map[string]string{"FOO": "bar"},
		WorktreeHostPath:  "/srv/aether/worktrees/run-1",
		WorktreeMountPath: "/workspace",
		WorkingDir:        "/workspace",
		Command:           []string{"sleep", "60"},
		SetupScript:       "echo hi",
		CPULimit:          1.5,
		MemoryLimitBytes:  64 << 20,
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{"valid", func(s *Spec) {}, ""},
		{"minimal", func(s *Spec) {
			*s = Spec{Image: "busybox", Command: []string{"true"}}
		}, ""},
		{"missing image", func(s *Spec) { s.Image = "" }, "image is required"},
		{"missing command", func(s *Spec) { s.Command = nil }, "command is required"},
		{"empty argv0", func(s *Spec) { s.Command = []string{""} }, "command[0]"},
		{"host path without mount path", func(s *Spec) { s.WorktreeMountPath = "" }, "set together"},
		{"mount path without host path", func(s *Spec) { s.WorktreeHostPath = "" }, "set together"},
		{"relative host path", func(s *Spec) { s.WorktreeHostPath = "worktrees/run-1" }, "must be absolute"},
		{"relative mount path", func(s *Spec) { s.WorktreeMountPath = "workspace" }, "must be absolute"},
		{"relative working dir", func(s *Spec) { s.WorkingDir = "workspace" }, "must be absolute"},
		{"negative cpu", func(s *Spec) { s.CPULimit = -1 }, "cpu limit"},
		{"negative memory", func(s *Spec) { s.MemoryLimitBytes = -1 }, "memory limit"},
		{"empty env key", func(s *Spec) { s.Env = map[string]string{"": "x"} }, "invalid env var name"},
		{"env key with equals", func(s *Spec) { s.Env = map[string]string{"A=B": "x"} }, "invalid env var name"},
		{"relative mount host path", func(s *Spec) {
			s.Mounts = []Mount{{HostPath: "homes/m1", ContainerPath: "/root/.claude"}}
		}, "must be absolute"},
		{"relative mount container path", func(s *Spec) {
			s.Mounts = []Mount{{HostPath: "/srv/homes/m1", ContainerPath: "root/.claude"}}
		}, "must be absolute"},
		{"valid mount", func(s *Spec) {
			s.Mounts = []Mount{{HostPath: "/srv/homes/m1", ContainerPath: "/root/.claude", ReadOnly: true}}
		}, ""},
		{"named user", func(s *Spec) { s.User = "node" }, "numeric uid:gid"},
		{"bare uid user", func(s *Spec) { s.User = "1000" }, "numeric uid:gid"},
		{"valid user", func(s *Spec) { s.User = "1000:1000" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validSpec()
			tt.mutate(&spec)
			err := spec.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSpecValidateJoinsAllErrors(t *testing.T) {
	err := Spec{CPULimit: -1}.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	for _, want := range []string{"image is required", "command is required", "cpu limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error %q missing %q", err, want)
		}
	}
}
