//go:build windows

package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A release asset the system cannot start - here bytes that are no
// executable, on a machine it is a blocked image - must not replace the
// working CLI.
func TestUpdateBinariesRestoresTheBuildWhenTheNewOneDoesNotStart(t *testing.T) {
	_, srv := newFakeRelease(t, "v1.3.0", map[string][]byte{
		"aether-" + runtime.GOOS + "-" + runtime.GOARCH + ".exe": []byte("not a PE file"),
	})
	self := filepath.Join(t.TempDir(), "aether.exe")
	if err := os.WriteFile(self, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := UpdateBinaries(t.Context(), srv.URL, "v1.3.0", self, "aether")
	if err == nil || !strings.Contains(err.Error(), "does not start, so the previous build was restored") {
		t.Fatalf("err = %v, want the new build refused and the old one restored", err)
	}
	assertUntouched(t, self)
	if _, err := os.Stat(self + ".old"); err == nil {
		t.Fatalf("%s.old left behind after the rollback", self)
	}
}
