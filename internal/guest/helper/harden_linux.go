package helper

import (
	"errors"
	"os"
)

// bootSysctls harden the guest kernel against an unprivileged in-VM agent,
// then give its network the two settings Docker sets in every container's
// network namespace. None has a compile-time default the config could set,
// so the helper writes them once /proc is mounted; docs/vm-guest.md#boot
// says what each does. unprivileged_bpf_disabled is set to 1, the kernel's
// one-way latch, not the config default of 2 that VM root could revert.
var bootSysctls = []struct{ path, value string }{
	{"/proc/sys/kernel/unprivileged_bpf_disabled", "1"},
	{"/proc/sys/kernel/kptr_restrict", "2"},
	{"/proc/sys/kernel/yama/ptrace_scope", "1"},
	{"/proc/sys/kernel/io_uring_disabled", "2"},
	{"/proc/sys/net/ipv4/ip_unprivileged_port_start", "0"},
	{"/proc/sys/net/ipv4/ping_group_range", "0 2147483647"},
}

// openFileLimit is the RLIMIT_NOFILE, soft and hard, the helper sets on
// itself and so on every process it starts, a Docker container's. The
// kernel's fs.nr_open, 1048576, caps it. unix.Setrlimit, unlike a raw
// prlimit, also stops Go from restoring the kernel's soft 1024 in children.
const openFileLimit = 524288

// writeBootSysctls writes bootSysctls. A setting whose file is absent,
// because the kernel lacks the feature, is skipped; any other write failure
// is returned, since a missing hardening step weakens the tier boundary.
func writeBootSysctls() error {
	var errs []error
	for _, s := range bootSysctls {
		if err := os.WriteFile(s.path, []byte(s.value), 0); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
