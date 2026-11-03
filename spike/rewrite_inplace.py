#!/usr/bin/env python3
"""rewrite_inplace.py <file> <access> <expires-in-seconds> [refresh] : truncate+write the SAME inode (no rename)"""
import json, sys, time, os
f, a, e = sys.argv[1], sys.argv[2], float(sys.argv[3])
o = {"accessToken": a, "expiresAt": int((time.time() + e) * 1000), "scopes": ["user:inference", "user:profile"], "subscriptionType": "max"}
if len(sys.argv) > 4: o["refreshToken"] = sys.argv[4]
before = os.stat(f).st_ino
with open(f, "r+") as h: h.seek(0); h.truncate(); h.write(json.dumps({"claudeAiOauth": o}))
print(time.strftime("%X"), "rewrote in place", a, "inode", before, "->", os.stat(f).st_ino, flush=True)
