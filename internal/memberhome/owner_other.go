//go:build !linux

package memberhome

import (
	"io/fs"
	"os"
)

func chownFileLikeHome(_ *os.Root, _ *os.File) error { return nil }

func chownLikeHomeAt(_, _ *os.Root, _ string) error { return nil }

func chownLikeHome(_ *os.Root, _ ...string) error { return nil }

func hasMultipleLinks(_ fs.FileInfo) bool { return false }
