//go:build !linux

package memberhome

import (
	"fmt"
	"os"
)

// Automatic home-cache migration requires descriptor-relative, no-replace
// rename support on the Linux server. Other development platforms preserve
// the old data rather than fall back to a symlink-racy path-based rename.
func renameCacheEntry(_ *os.Root, _ string, _ *os.Root, _ string) error {
	return fmt.Errorf("memberhome: safe legacy cache migration requires Linux")
}
