//go:build !unix

package main

import "io/fs"

// checkOwner has no owner to compare where files carry no uid.
func checkOwner(string, fs.FileInfo) error { return nil }
