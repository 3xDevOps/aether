//go:build !unix

package disk

import "errors"

// filesystem has no portable answer off unix. The server ships for linux
// only; this exists so the package builds - and reports honestly - wherever
// the tooling runs.
func filesystem(string) (Usage, error) {
	return Usage{}, errors.New("disk: filesystem usage is not supported on this platform")
}
