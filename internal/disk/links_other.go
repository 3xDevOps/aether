//go:build !unix && !windows

package disk

import (
	"io/fs"
	"os"
)

// Platforms without a public indexed file identity use the portable comparison.
type seen struct{ files []fs.FileInfo }

func newSeen(_ *os.Root) seen { return seen{} }

func (s *seen) claim(_ string, info fs.FileInfo) (bool, error) {
	for _, previous := range s.files {
		if os.SameFile(previous, info) {
			return false, nil
		}
	}
	s.files = append(s.files, info)
	return true, nil
}
