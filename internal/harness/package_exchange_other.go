//go:build !linux

package harness

import (
	"errors"
	"os"
)

// ExchangePackages requires Linux atomic directory exchange.
func ExchangePackages(installed, staged string) error {
	return &os.LinkError{Op: "exchange", Old: installed, New: staged, Err: errors.ErrUnsupported}
}
