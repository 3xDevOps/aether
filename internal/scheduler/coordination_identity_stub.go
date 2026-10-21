//go:build !linux

package scheduler

import "io/fs"

// fileIdentity is unknown off Linux, so every stage hashes the staged copy.
type fileIdentity struct{}

func identityOf(fs.FileInfo) fileIdentity { return fileIdentity{} }
