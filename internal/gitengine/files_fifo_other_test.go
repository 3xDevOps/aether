//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package gitengine

import "errors"

func makeFIFO(string) error {
	return errors.New("FIFO is unsupported on this platform")
}
