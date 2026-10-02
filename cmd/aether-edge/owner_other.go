//go:build !unix

package main

import (
	"fmt"
	"io/fs"
	"os"
)

// checkOwner has no owner to compare where files carry no uid.
func checkOwner(string, fs.FileInfo) error { return nil }

// checkDataDir creates the data directory when it is missing.
func checkDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("data directory: %w; name another with --data or AETHER_EDGE_DATA", err)
	}
	return nil
}
