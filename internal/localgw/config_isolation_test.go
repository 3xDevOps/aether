package localgw

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestLocalConfigTestsLeaveUserConfigAlone(t *testing.T) {
	for _, inheritedOverride := range []bool{false, true} {
		name := "platform path"
		if inheritedOverride {
			name = "inherited override"
		}
		t.Run(name, func(t *testing.T) {
			testhome.Isolate(t)
			if !inheritedOverride {
				t.Setenv(cli.ConfigDirEnv, "")
			}
			path, err := cli.Path()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			const sentinel = "{\"addr\":\"sentinel:2222\"}\n"
			if err = os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			t.Run("refresh", TestLocalSnapshotKeepsCachedNamedConfigWhenProfileDisappears)
			t.Run("link repo", TestLocalLinkRepoKeepsNewRepoForActiveNamedProfile)

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != sentinel {
				t.Errorf("gateway tests overwrote user config: %s", body)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !after.ModTime().Equal(before.ModTime()) {
				t.Error("gateway tests changed user config mtime")
			}
		})
	}
}
