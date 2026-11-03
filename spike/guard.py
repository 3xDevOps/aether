#!/usr/bin/env python3
"""Run argv under a seccomp+ptrace egress guard (connect() is RET_TRACE'd; tracer reads the sockaddr) (no root, no netns needed).
connect(): allowed only for AF_UNIX and 127.0.0.0/8 (AF_INET); everything else -> EACCES and logged to $GUARD_LOG.
socket(AF_INET/AF_INET6, SOCK_DGRAM|SOCK_RAW), socket(AF_PACKET), io_uring_setup -> EPERM (no UDP/DNS/raw/QUIC).
usage: guard.py <cmd...>"""
import ctypes, os, sys, struct, socket, json, time, signal
LOG = os.environ.get("GUARD_LOG", "/tmp/spike-run/guard.log")
libc = ctypes.CDLL(None, use_errno=True)
NR_SOCKET, NR_CONNECT, NR_IO_URING = 41, 42, 425
ALLOW, USER_NOTIF = 0x7FFF0000, 0x7FF00000
def ERR(e): return 0x00050000 | e
def ins(code, jt, jf, k): return struct.pack("HBBI", code, jt, jf, k)
LD, JEQ, RET, AND = 0x20, 0x15, 0x06, 0x54
def asm(items):
    labels, out, n = {}, [], 0
    for it in items:
        if isinstance(it, str): labels[it] = n
        else: n += 1
    i = 0
    for it in items:
        if isinstance(it, str): continue
        code, k, jt, jf = it
        i += 1
        if code == JEQ: jt = labels[jt] - i if isinstance(jt, str) else jt; jf = labels[jf] - i if isinstance(jf, str) else jf
        out.append(ins(code, jt, jf, k))
    return out
prog = asm([
 (LD,4,0,0), (JEQ,0xC000003E,0,"deny"),
 (LD,0,0,0),
 (JEQ,NR_CONNECT,"notif",0), (JEQ,NR_IO_URING,"deny",0), (JEQ,NR_SOCKET,"sock","allow"),
 "sock", (LD,16,0,0), (JEQ,2,"typ",0), (JEQ,10,"typ",0), (JEQ,17,"deny","allow"),
 "typ", (LD,24,0,0), (AND,0xf,0,0), (JEQ,2,"deny",0), (JEQ,3,"deny","allow"),
 "allow", (RET,ALLOW,0,0), "deny", (RET,ERR(1),0,0), "notif", (RET,USER_NOTIF,0,0),
])
def install():
    libc.prctl(38, 1, 0, 0, 0)
    buf = ctypes.create_string_buffer(b"".join(prog)); fpb = ctypes.create_string_buffer(struct.pack("HxxxxxxP", len(prog), ctypes.addressof(buf)))
    if libc.syscall(317, 1, 0, fpb) < 0: sys.exit("seccomp failed errno %d" % ctypes.get_errno())
PTRACE_TRACEME, PEEK, CONT, GETREGS, SETREGS, SETOPTIONS, KILL = 0, 2, 7, 12, 13, 0x4200, 8
libc.ptrace.restype = ctypes.c_long; libc.ptrace.argtypes = [ctypes.c_long, ctypes.c_long, ctypes.c_void_p, ctypes.c_void_p]
class Regs(ctypes.Structure):
    _fields_ = [(n, ctypes.c_ulonglong) for n in "r15 r14 r13 r12 rbp rbx r11 r10 r9 r8 rax rcx rdx rsi rdi orig_rax rip cs eflags rsp ss fs_base gs_base ds es fs gs".split()]
pid = os.fork()
if pid == 0:
    libc.ptrace(PTRACE_TRACEME, 0, None, None); install()
    os.kill(os.getpid(), signal.SIGSTOP); os.execvp(sys.argv[1], sys.argv[1:])
logf = open(LOG, "a", buffering=1)
def log(**r): r["t"] = round(time.time(), 3); logf.write(json.dumps(r) + "\n")
os.waitpid(pid, 0)
# TRACESECCOMP|TRACECLONE|TRACEFORK|TRACEVFORK|TRACEEXEC|EXITKILL
libc.ptrace(SETOPTIONS, pid, None, ctypes.c_void_p(0x80 | 0x40 | 0x8 | 0x4 | 0x2 | 0x100000)); libc.ptrace(CONT, pid, None, None)
while True:
    try: p, st = os.waitpid(-1, 0x40000000)
    except ChildProcessError: break
    if os.WIFEXITED(st) or os.WIFSIGNALED(st):
        if p == pid: sys.exit(os.waitstatus_to_exitcode(st))
        continue
    sig = os.WSTOPSIG(st); ev = st >> 16; deliver = 0
    if ev == 7:
        r = Regs(); libc.ptrace(GETREGS, p, None, ctypes.addressof(r))
        try:
            with open(f"/proc/{p}/mem", "rb") as m: m.seek(r.rsi); sa = m.read(min(r.rdx, 128))
            fam = struct.unpack("H", sa[:2])[0]; ok = False
            if fam == 1: dst, ok = "unix", True
            elif fam == 2: ip = socket.inet_ntoa(sa[4:8]); dst = f"{ip}:{struct.unpack('>H', sa[2:4])[0]}"; ok = ip.startswith("127.")
            elif fam == 10: dst = "[" + socket.inet_ntop(socket.AF_INET6, sa[8:24]) + "]"; ok = dst == "[::1]"
            else: dst = f"family{fam}"; ok = fam == 16
        except Exception as e: dst, ok = f"err:{e}", False
        log(connect=dst, allowed=ok, pid=p)
        if not ok:
            r.orig_rax = ctypes.c_ulonglong(-1).value; r.rax = ctypes.c_ulonglong(-13).value; libc.ptrace(SETREGS, p, None, ctypes.addressof(r))
    elif ev == 0 and sig not in (signal.SIGTRAP, signal.SIGSTOP): deliver = sig
    libc.ptrace(CONT, p, None, ctypes.c_void_p(deliver))
