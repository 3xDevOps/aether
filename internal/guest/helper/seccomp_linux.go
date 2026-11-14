package helper

import (
	"fmt"
	goruntime "runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// denyVsock installs a seccomp filter that fails socket and socketpair of
// AF_VSOCK with EPERM, for this thread and what it executes. vsock is not
// namespaced: a process in the VM could otherwise reach every host socket
// the VM forwards, the session's coordination socket among them. io_uring
// can make sockets without either call, so io_uring_setup fails with ENOSYS,
// which programs that try it fall back from. The filter kills a process that
// makes a call of another architecture, and refuses x86-64's x32 calls,
// whose numbers it does not check.
func denyVsock() error {
	arch, ok := map[string]uint32{"amd64": unix.AUDIT_ARCH_X86_64, "arm64": unix.AUDIT_ARCH_AARCH64}[goruntime.GOARCH]
	if !ok {
		return fmt.Errorf("no seccomp filter for %s", goruntime.GOARCH)
	}
	const (
		load    = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		equals  = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		atLeast = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
		ret     = unix.BPF_RET | unix.BPF_K
		// Offsets in struct seccomp_data; the first argument's low word on
		// these little-endian architectures.
		nr, archOffset, arg0 = 0, 4, 16
		x32                  = 0x40000000
	)
	deny := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)
	noSys := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.ENOSYS)
	filter := []unix.SockFilter{
		{Code: load, K: archOffset},
		{Code: equals, Jt: 1, K: arch},
		{Code: ret, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: load, K: nr},
		{Code: atLeast, Jt: 7, K: x32},
		{Code: equals, Jt: 7, K: unix.SYS_IO_URING_SETUP},
		{Code: equals, Jt: 2, K: unix.SYS_SOCKET},
		{Code: equals, Jt: 1, K: unix.SYS_SOCKETPAIR},
		{Code: ret, K: unix.SECCOMP_RET_ALLOW},
		{Code: load, K: arg0},
		{Code: equals, Jt: 1, K: unix.AF_VSOCK},
		{Code: ret, K: unix.SECCOMP_RET_ALLOW},
		{Code: ret, K: deny},
		{Code: ret, K: noSys},
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0)
}
