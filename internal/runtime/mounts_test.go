package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mountEnv is a filesystem fixture for validator tests: an owned root with
// a credential home, a worktree checkout, and paths outside the root.
type mountEnv struct {
	root     string // aether-owned root (e.g. <data>/homes)
	credDir  string // <root>/member/claude
	worktree string // a checkout outside root
	outside  string // a directory outside every owned root
}

func newMountEnv(t *testing.T) mountEnv {
	t.Helper()
	base := t.TempDir()
	e := mountEnv{
		root:     filepath.Join(base, "homes"),
		worktree: filepath.Join(base, "checkouts", "run-1"),
		outside:  filepath.Join(base, "elsewhere"),
	}
	e.credDir = filepath.Join(e.root, "member", "claude")
	for _, dir := range []string{e.credDir, e.worktree, e.outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e mountEnv) policy() MountPolicy {
	return MountPolicy{
		OwnedRoots:        []string{e.root},
		WorktreeHostPath:  e.worktree,
		WorktreeMountPath: "/workspace",
	}
}

func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestValidateMountsAccepts(t *testing.T) {
	e := newMountEnv(t)
	sub := filepath.Join(e.credDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mounts []Mount
		policy MountPolicy
	}{
		{"no mounts", nil, e.policy()},
		{"single credential home", []Mount{
			{HostPath: e.credDir, ContainerPath: "/root/.claude"},
		}, e.policy()},
		{"read-only leaf", []Mount{
			{HostPath: e.credDir, ContainerPath: "/etc/aether-profile", ReadOnly: true},
		}, e.policy()},
		{"disjoint targets", []Mount{
			{HostPath: e.credDir, ContainerPath: "/root/.claude"},
			{HostPath: sub, ContainerPath: "/root/.config"},
		}, e.policy()},
		{"approved nesting child after parent", []Mount{
			{HostPath: e.credDir, ContainerPath: "/root/.claude"},
			{HostPath: sub, ContainerPath: "/root/.claude/creds"},
		}, MountPolicy{
			OwnedRoots:        []string{e.root},
			WorktreeHostPath:  e.worktree,
			WorktreeMountPath: "/workspace",
			AllowedNestings:   map[string]string{"/root/.claude/creds": "/root/.claude"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateMounts(tt.mounts, tt.policy); err != nil {
				t.Fatalf("ValidateMounts = %v, want nil", err)
			}
		})
	}
}

func TestValidateMountsRejects(t *testing.T) {
	e := newMountEnv(t)
	sub := filepath.Join(e.credDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(e.credDir, "settings.json")
	if err := os.WriteFile(regular, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		mounts  []Mount
		policy  MountPolicy
		wantErr string // empty means any error is acceptable
	}{
		{"relative target", []Mount{
			{HostPath: e.credDir, ContainerPath: "root/.claude"},
		}, e.policy(), "must be absolute"},
		{"root target", []Mount{
			{HostPath: e.credDir, ContainerPath: "/"},
		}, e.policy(), "container root"},
		{"uncanonical root target", []Mount{
			{HostPath: e.credDir, ContainerPath: "/root/.."},
		}, e.policy(), "container root"},
		{"reserved run target", []Mount{
			{HostPath: e.credDir, ContainerPath: "/run/aether"},
		}, e.policy(), "reserved"},
		{"reserved run subpath", []Mount{
			{HostPath: e.credDir, ContainerPath: "/run/aether/mailbox"},
		}, e.policy(), "reserved"},
		{"reserved opt subpath", []Mount{
			{HostPath: e.credDir, ContainerPath: "/opt/aether/bin"},
		}, e.policy(), "reserved"},
		{"target equals worktree", []Mount{
			{HostPath: e.credDir, ContainerPath: "/workspace"},
		}, e.policy(), "worktree"},
		{"target under worktree", []Mount{
			{HostPath: e.credDir, ContainerPath: "/workspace/.claude"},
		}, e.policy(), "worktree"},
		{"target containing worktree", []Mount{
			{HostPath: e.credDir, ContainerPath: "/works"},
		}, MountPolicy{
			OwnedRoots:        []string{e.root},
			WorktreeHostPath:  e.worktree,
			WorktreeMountPath: "/works/space",
		}, "worktree"},
		{"relative source", []Mount{
			{HostPath: "homes/member", ContainerPath: "/root/.claude"},
		}, e.policy(), "must be absolute"},
		{"missing source", []Mount{
			{HostPath: filepath.Join(e.root, "missing"), ContainerPath: "/root/.claude"},
		}, e.policy(), ""},
		{"source outside owned roots", []Mount{
			{HostPath: e.outside, ContainerPath: "/root/.claude"},
		}, e.policy(), "outside every aether-owned root"},
		{"source is worktree", []Mount{
			{HostPath: e.worktree, ContainerPath: "/root/.claude"},
		}, MountPolicy{
			OwnedRoots:        []string{filepath.Dir(e.worktree)},
			WorktreeHostPath:  e.worktree,
			WorktreeMountPath: "/workspace",
		}, "worktree checkout"},
		{"duplicate target", []Mount{
			{HostPath: e.credDir, ContainerPath: "/root/.claude"},
			{HostPath: sub, ContainerPath: "/root/.claude"},
		}, e.policy(), "duplicate target"},
		{"unapproved nested target", []Mount{
			{HostPath: e.credDir, ContainerPath: "/root/.claude"},
			{HostPath: sub, ContainerPath: "/root/.claude/creds"},
		}, e.policy(), "without approval"},
		{"nested child ordered before parent", []Mount{
			{HostPath: sub, ContainerPath: "/root/.claude/creds"},
			{HostPath: e.credDir, ContainerPath: "/root/.claude"},
		}, MountPolicy{
			OwnedRoots:        []string{e.root},
			WorktreeHostPath:  e.worktree,
			WorktreeMountPath: "/workspace",
			AllowedNestings:   map[string]string{"/root/.claude/creds": "/root/.claude"},
		}, "ordered after its parent"},
		{"approved nesting under regular-file parent", []Mount{
			{HostPath: regular, ContainerPath: "/root/.claude"},
			{HostPath: sub, ContainerPath: "/root/.claude/creds"},
		}, MountPolicy{
			OwnedRoots:        []string{e.root},
			WorktreeHostPath:  e.worktree,
			WorktreeMountPath: "/workspace",
			AllowedNestings:   map[string]string{"/root/.claude/creds": "/root/.claude"},
		}, "not a directory"},
		{"read-only source contains nested mount", []Mount{
			{HostPath: e.credDir, ContainerPath: "/a", ReadOnly: true},
			{HostPath: sub, ContainerPath: "/b"},
		}, e.policy(), "read-only"},
		{"read-only source over earlier mount", []Mount{
			{HostPath: sub, ContainerPath: "/a"},
			{HostPath: e.credDir, ContainerPath: "/b", ReadOnly: true},
		}, e.policy(), "read-only"},
		{"no owned roots rejects everything", []Mount{
			{HostPath: e.credDir, ContainerPath: "/root/.claude"},
		}, MountPolicy{}, "outside every aether-owned root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMounts(tt.mounts, tt.policy)
			if err == nil {
				t.Fatalf("ValidateMounts = nil, want error containing %q", tt.wantErr)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateMounts = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMountsSymlinkEscape(t *testing.T) {
	e := newMountEnv(t)
	link := filepath.Join(e.credDir, "escape")
	symlink(t, e.outside, link)
	err := ValidateMounts([]Mount{
		{HostPath: link, ContainerPath: "/root/.claude"},
	}, e.policy())
	if err == nil || !strings.Contains(err.Error(), "outside every aether-owned root") {
		t.Fatalf("ValidateMounts = %v, want symlink escape rejection", err)
	}
}

// TestValidateMountsCanonicalizesHostPath pins the TOCTOU narrowing:
// validation rewrites each accepted mount's HostPath to the resolved
// source it actually checked, so the bind handed to Docker cannot differ
// from the validated path via a symlink swapped after validation.
func TestValidateMountsCanonicalizesHostPath(t *testing.T) {
	e := newMountEnv(t)
	link := filepath.Join(e.root, "member", "alias")
	symlink(t, e.credDir, link)
	resolved, err := filepath.EvalSymlinks(e.credDir)
	if err != nil {
		t.Fatal(err)
	}
	mounts := []Mount{{HostPath: link, ContainerPath: "/root/.claude"}}
	if err := ValidateMounts(mounts, e.policy()); err != nil {
		t.Fatalf("ValidateMounts = %v, want nil", err)
	}
	if mounts[0].HostPath != resolved {
		t.Errorf("post-validation HostPath = %q, want resolved %q", mounts[0].HostPath, resolved)
	}
}

func TestValidateMountsSymlinkedWorktreeCollision(t *testing.T) {
	e := newMountEnv(t)
	link := filepath.Join(e.credDir, "wt")
	symlink(t, e.worktree, link)
	err := ValidateMounts([]Mount{
		{HostPath: link, ContainerPath: "/root/.claude"},
	}, MountPolicy{
		// The worktree parent is also owned so the owned-root check cannot
		// mask the collision check.
		OwnedRoots:        []string{e.root, filepath.Dir(e.worktree)},
		WorktreeHostPath:  e.worktree,
		WorktreeMountPath: "/workspace",
	})
	if err == nil || !strings.Contains(err.Error(), "worktree checkout") {
		t.Fatalf("ValidateMounts = %v, want worktree collision", err)
	}
}

func TestValidateMountsDockerSocket(t *testing.T) {
	e := newMountEnv(t)
	// Stand-in socket paths inside the owned root so the owned-root check
	// cannot mask the socket check; one alias symlinks to the other, as
	// /var/run does to /run.
	varRun := filepath.Join(e.root, "var", "run")
	run := filepath.Join(e.root, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(varRun), 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(run, "docker.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, run, varRun)

	orig := dockerSocketPaths
	dockerSocketPaths = []string{filepath.Join(varRun, "docker.sock"), sock}
	t.Cleanup(func() { dockerSocketPaths = orig })

	tests := []struct {
		name   string
		source string
	}{
		{"socket itself", sock},
		{"socket via alias", filepath.Join(varRun, "docker.sock")},
		{"ancestor directory", run},
		{"alias ancestor directory", varRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMounts([]Mount{
				{HostPath: tt.source, ContainerPath: "/mnt/x"},
			}, e.policy())
			if err == nil || !strings.Contains(err.Error(), "docker socket") {
				t.Fatalf("ValidateMounts(%s) = %v, want docker socket rejection", tt.source, err)
			}
		})
	}
}
