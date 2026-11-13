#!/usr/bin/env bash
# Per-session host memory (MiB) per phase from q1-memory.sh samples. VM:
# monitor RssAnon+RssShmem plus virtiofsd RssAnon (its RSS also counts the guest pages it maps). Container: cgroup anon+shmem
# +kernel, and file (page cache, shared with other containers). npm is the
# per-session peak over its samples. host_delta: MemAvailable drop since
# "before", divided by the session count (noisy: other tenants share the host).
awk '
function kv(   i, p) { delete f; for (i = 3; i <= NF; i++) { split($i, p, "="); sub(":$", "", p[1]); f[p[1]] = p[2] } }
{
  split($1, ph, "="); phase = ph[2]; kv()
  if ($3 == "host") {
    if (phase == "before") base = f["MemAvailable"]
    if (!(phase in hostmin) || f["MemAvailable"] < hostmin[phase]) hostmin[phase] = f["MemAvailable"]
    next
  }
  s = $3
  if (s ~ /^vm/) { own = (f["RssAnon"] + f["RssShmem"] + f["virtiofsd_anon"]) / 1024; file = 0 }
  else { own = (f["anon"] + f["shmem"] + f["kernel"]) / 1024; file = f["file"] / 1024 }
  sessions[s] = 1
  if (!((phase, s) in peak) || own > peak[phase, s]) { peak[phase, s] = own; pfile[phase, s] = file }
  phases[phase] = 1
}
END {
  n = 0; for (s in sessions) n++
  split("booted idle npm after reclaimed later", order, " ")
  printf "%-8s %10s %10s %10s %12s\n", "phase", "own_avg", "own_max", "file_avg", "host_delta"
  for (k = 1; k <= 6; k++) {
    p = order[k]; if (!(p in phases)) continue
    sum = 0; mx = 0; fs = 0
    for (s in sessions) { sum += peak[p, s]; fs += pfile[p, s]; if (peak[p, s] > mx) mx = peak[p, s] }
    printf "%-8s %10.0f %10.0f %10.0f %12.0f\n", p, sum / n, mx, fs / n, (base - hostmin[p]) / 1024 / n
  }
}' "$1"
