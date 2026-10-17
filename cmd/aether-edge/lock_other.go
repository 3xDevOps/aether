//go:build !unix

package main

// lockData has no lock to take where the edge does not run.
func lockData(string) (func(), error) { return func() {}, nil }
