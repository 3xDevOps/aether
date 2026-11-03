#!/usr/bin/env python3
"""romount.py <file> -- <cmd...> : run cmd in a private user+mount ns where <file> is bind-mounted read-only over itself
(same semantics as `docker run -v file:file:ro`: writes EROFS, rename/unlink over it EBUSY)."""
import ctypes, os, sys
libc = ctypes.CDLL(None, use_errno=True)
f = os.path.realpath(sys.argv[1]); cmd = sys.argv[sys.argv.index("--") + 1:]
uid, gid = os.getuid(), os.getgid()
if libc.unshare(0x10000000 | 0x00020000): sys.exit("unshare errno %d" % ctypes.get_errno())
try: open("/proc/self/setgroups", "w").write("deny")
except OSError: pass
open("/proc/self/uid_map", "w").write(f"{uid} {uid} 1"); open("/proc/self/gid_map", "w").write(f"{gid} {gid} 1")
MS_BIND, MS_REMOUNT, MS_RDONLY, MS_REC = 4096, 32, 1, 16384
def mount(src, tgt, fl, data=None):
    if libc.mount(src.encode() if src else None, tgt.encode(), None, ctypes.c_ulong(fl), data) != 0: sys.exit(f"mount {tgt} failed errno {ctypes.get_errno()}")
mount(f, f, MS_BIND); mount(None, f, MS_BIND | MS_REMOUNT | MS_RDONLY)
os.execvp(cmd[0], cmd)
