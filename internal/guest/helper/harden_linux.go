package helper

import (
	"errors"
	"os"
)

// bootSysctls are the runtime kernel settings the helper writes once /proc
// is mounted, to shrink a Default-tier uid-1000 agent's in-VM escalation
// surface. Each has no compile-time default knob (kptr_restrict,
// yama.ptrace_scope and io_uring_disabled are 0 at boot) or needs a value
// the config cannot set, so they are set here rather than in the kernel
// config (images/kernel/config-x86_64).
//
//   - unprivileged_bpf_disabled=1 turns unprivileged BPF off and latches: the
//     kernel refuses to set it back to 0, so not even root in the VM can
//     re-enable it. CONFIG_BPF_UNPRIV_DEFAULT_OFF only starts it at 2, which
//     root could revert.
//   - kptr_restrict=2 hides every kernel pointer in /proc (kallsyms, module
//     addresses) from all users, so KASLR and heap layout stay secret.
//   - yama.ptrace_scope=1 lets a process ptrace only its own descendants.
//   - io_uring_disabled=2 removes io_uring, a recurring privilege-escalation
//     surface, for everyone; the browser already blocks it with seccomp and
//     the harnesses fall back when io_uring_setup returns ENOSYS.
//   - perf_event_paranoid=3 denies unprivileged use of perf_event_open.
var bootSysctls = []struct{ path, value string }{
	{"/proc/sys/kernel/unprivileged_bpf_disabled", "1"},
	{"/proc/sys/kernel/kptr_restrict", "2"},
	{"/proc/sys/kernel/yama/ptrace_scope", "1"},
	{"/proc/sys/kernel/io_uring_disabled", "2"},
	{"/proc/sys/kernel/perf_event_paranoid", "3"},
}

// harden writes bootSysctls. A setting whose file is absent, because the
// kernel lacks the feature, is skipped; any other write failure is
// returned, since a missing hardening step weakens the tier boundary.
func harden() error {
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
