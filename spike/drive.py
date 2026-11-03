#!/usr/bin/env python3
"""drive.py <log> <script.json> -- run interactive claude under a pty+guard; script = [[delay_s,"text-to-send"],...]; ends after last step + settle.
env: CMD (shell command), SETTLE seconds"""
import os, pty, sys, time, select, json, re, subprocess, signal
log, script = sys.argv[1], json.load(open(sys.argv[2]))
cmd = os.environ["CMD"]
pid, fd = pty.fork()
if pid == 0:
    os.execvp("bash", ["bash", "-c", cmd])
import fcntl, termios, struct
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 160, 0, 0))
out = open(log, "wb"); t0 = time.time(); steps = list(script); nxt = t0 + steps[0][0] if steps else None
end = None
def pump(until):
    while time.time() < until:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try: d = os.read(fd, 65536)
            except OSError: return False
            if not d: return False
            out.write(d); out.flush()
    return True
alive = True
while steps and alive:
    delay, text = steps.pop(0); alive = pump(time.time() + delay)
    if alive: os.write(fd, text.encode().decode("unicode_escape").encode()); out.write(f"\n<<SENT {text!r} at +{time.time()-t0:.1f}s>>\n".encode())
if alive: pump(time.time() + float(os.environ.get("SETTLE", 5)))
try: os.kill(pid, signal.SIGKILL)
except Exception: pass
