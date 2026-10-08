//go:build !linux

package disk

import (
	"io/fs"
	"os"
)

// The non-Linux tooling fallback uses the portable file-identity comparison.
type seen struct{ files []fs.FileInfo }

func newSeen() *seen { return &seen{} }

func (s *seen) claim(info fs.FileInfo) bool {
	for _, previous := range s.files {
		if os.SameFile(previous, info) {
			return false
		}
	}
	s.files = append(s.files, info)
	return true
}
