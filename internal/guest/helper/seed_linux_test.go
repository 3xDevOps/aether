package helper

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/guest"
)

func TestASeededStateMountCopiesTheHomesFilesAtBoot(t *testing.T) {
	volume, target := t.TempDir(), t.TempDir()
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(target, "login"), "fake-login")
	write(filepath.Join(target, "cache", "onboarding.json"), `{"onboardingComplete":true}`)
	write(filepath.Join(volume, "0", "settings.json"), "stale")
	m := guest.Mount{Volume: guest.VolumeState, Source: "0", Target: target, Seed: []string{"login", "cache/onboarding.json", "settings.json"}}
	if err := seed(volume, m); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"login": "fake-login", "cache/onboarding.json": `{"onboardingComplete":true}`} {
		got, err := os.ReadFile(filepath.Join(volume, "0", name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(volume, "0", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("a copy whose file the home no longer has: %v", err)
	}
	if info, err := os.Stat(filepath.Join(volume, "0", "cache")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("seeded folder: %v, %v", info, err)
	}
}
