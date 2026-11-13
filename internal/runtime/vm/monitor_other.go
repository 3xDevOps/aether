//go:build !linux

package vm

import "errors"

func Monitor([]string) error {
	return errors.New("vm monitor: session VMs run only on Linux")
}
