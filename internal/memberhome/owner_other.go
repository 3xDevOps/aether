//go:build !linux

package memberhome

import (
	"io/fs"
	"os"
)

func homeOwner(_ *os.Root) (int, int, bool, error) { return 0, 0, false, nil }

func chownFileLikeHome(_ *os.Root, _ *os.File) error { return nil }

func chownLikeHomeAt(_, _ *os.Root, _ string) error { return nil }

func chownLikeHome(_ *os.Root, _ ...string) error { return nil }

func hasMultipleLinks(_ fs.FileInfo) bool { return false }
