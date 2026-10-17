//go:build !unix

package main

const (
	exclusive = iota
	shared
)

// lockPath has no lock to take where the edge does not run.
func lockPath(string, int) (func(), error) { return func() {}, nil }
