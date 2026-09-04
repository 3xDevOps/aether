//go:build !linux

package disk

import "io/fs"

// seen has no way to recognise a hardlink off linux, so every pathname
// counts and a checkout's object files are charged twice. The server ships
// for linux; this exists so the package builds - and over-reports rather
// than lying about which tree holds what - wherever the tooling runs.
type seen struct{}

func newSeen() seen { return seen{} }

func (seen) claim(fs.FileInfo) bool { return true }
