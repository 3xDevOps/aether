//go:build !linux

package memberhome

import "os"

// chownLikeHome needs Unix ownership semantics; the server only ships for
// Linux, and a non-Linux development host runs everything as one uid.
func chownLikeHome(_ *os.Root, _ ...string) error { return nil }
