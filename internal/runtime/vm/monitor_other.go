//go:build !linux

package vm

import "errors"

func Monitor([]string) error {
	return errors.New("vm monitor: session VMs run only on Linux")
}

type IDMap struct {
	OnDiskUID, OnDiskGID int
	ShownUID, ShownGID   int
}

func BindBeneath(string, string, string, IDMap) error {
	return errors.New("vm: binding beneath a directory needs Linux")
}

func Unbind(string) error {
	return errors.New("vm: unbinding needs Linux")
}
