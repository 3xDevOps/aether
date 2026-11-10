//go:build !linux

package cli

import "syscall"

var dialControl func(network, address string, c syscall.RawConn) error
