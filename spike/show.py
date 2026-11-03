#!/usr/bin/env python3
"""summarise mock.log: t+sec METHOD path status token beta (event_logging/proxy lines hidden unless -a)"""
import json, sys, os
D = os.environ.get("SPIKE_DIR", "/tmp/spike-run"); allv = "-a" in sys.argv; full = "-f" in sys.argv
t0 = None
for l in open(D + "/mock.log"):
    r = json.loads(l); t0 = t0 or r["t"]
    if "m" not in r:
        if allv: print(f"+{r['t']-t0:7.1f}", {k: v for k, v in r.items() if k != "t"})
        continue
    if not allv and ("event_logging" in r["path"] or "eval/sdk" in r["path"]): continue
    h = r["hdr"]; tok = h.get("authorization", "-").replace("Bearer ", "B:") if "authorization" in h else ("X-API-KEY:" + h["x-api-key"] if "x-api-key" in h else "-")
    beta = h.get("anthropic-beta", "-"); beta = beta if full else beta[:60]
    print(f"+{r['t']-t0:7.1f} {r['m']:4} {r['host']:15} {r['path'][:60]:60} {r['status']} {tok} beta={beta} {r.get('body_keys','')}")
