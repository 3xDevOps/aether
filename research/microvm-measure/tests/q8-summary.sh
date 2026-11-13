#!/usr/bin/env bash
# Per-session host memory (MiB, average over sessions) per phase from
# q8-enhanced.sh samples: VM monitor RssAnon+RssShmem, container cgroup
# anon+shmem+kernel (+ file cache). "settled after" is the first settle
# sample from which every later one is within 3% of the last. ept: the
# host's SecPageTables growth from before to booted, per session (other
# tenants move it too).
awk '
function kv(   i, p) { delete f; for (i = 3; i <= NF; i++) { split($i, p, "="); sub(":$", "", p[1]); f[p[1]] = p[2] } }
{
  split($1, ph, "="); phase = ph[2]; kv()
  if ($3 == "host") { sec[phase] = f["SecPageTables"]; next }
  if ($3 ~ /^vm/) { own = (f["RssAnon"] + f["RssShmem"]) / 1024; file = 0 }
  else { own = (f["anon"] + f["shmem"] + f["kernel"]) / 1024; file = f["file"] / 1024 }
  sum[phase] += own; fsum[phase] += file; cnt[phase]++
  if (!(phase in seen)) { seen[phase] = 1; order[++np] = phase }
}
END {
  for (k = 1; k <= np; k++) { p = order[k]; avg[p] = sum[p] / cnt[p] }
  printf "%-10s %8s %8s\n", "phase", "own", "file"
  for (k = 1; k <= np; k++) {
    p = order[k]
    if (p ~ /^settle/ && p !~ /^settle(5|10|20|30|60|90|120|180)$/) continue
    printf "%-10s %8.0f %8.0f\n", p, avg[p], fsum[p] / cnt[p]
  }
  last = avg["settle180"]; settled = ""
  for (k = np; k >= 1; k--) {
    p = order[k]; if (p !~ /^settle/) break
    if (avg[p] > last * 1.03 || avg[p] < last * 0.97) break
    settled = p
  }
  sub("settle", "", settled)
  n = cnt["booted"]
  printf "settled after %s s at %.0f MiB; ept %.1f MiB/session\n", settled, last, (sec["booted"] - sec["before"]) / 1024 / n
}' "$1"
