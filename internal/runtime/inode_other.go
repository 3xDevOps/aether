//go:build !unix

package runtime

import (
	"errors"
	"io/fs"
)

func fileInode(fs.FileInfo) (uint64, error) {
	return 0, errors.New("reading an inode needs a unix host")
}
