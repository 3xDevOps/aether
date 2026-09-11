package memberhome

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestConfigWritePreservesModeWithRestrictiveUmask(t *testing.T) {
	if os.Getenv("AETHER_TEST_CONFIG_UMASK") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigWritePreservesModeWithRestrictiveUmask$")
		cmd.Env = append(os.Environ(), "AETHER_TEST_CONFIG_UMASK=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated umask regression: %v\n%s", err, output)
		}
		return
	}

	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "member-a", ".claude", "check.sh")
	if err = os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, []byte("#!/bin/sh\necho before\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(name, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := manager.ConfigRead(context.Background(), "member-a", "claude", ".claude", "check.sh", nil)
	if err != nil {
		t.Fatal(err)
	}

	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	content := "#!/bin/sh\necho after\n"
	if _, err = manager.ConfigWrite(context.Background(), "member-a", "claude", ".claude", "check.sh", []byte(content), before.Revision, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("saved mode = %04o, want 0755", info.Mode().Perm())
	}
	got, err := os.ReadFile(name)
	if err != nil || string(got) != content {
		t.Fatalf("saved content = %q, err=%v", got, err)
	}
}
