#!/usr/bin/env bash
# Prints the result lines of every run under /srv/aether/1/results, for
# results.md.
cd /srv/aether/1/results || exit 1
for d in q1-*; do echo "== $d"; cat "$d/summary.txt" 2>/dev/null; grep -hE '^(booted|session [0-9]+ npm:|ksmd)' "$d/log.txt"; done
for d in q2-*; do echo "== $d"; grep -hE '^(baseline|before|save|snapshot size|restore|  30 s)' "$d/log.txt" | cut -c1-240; done
for d in q3-* q4-*; do echo "== $d"; grep -hE '^(ok|FAIL)' "$d/log.txt" | cut -c1-160; done
for d in q5-* q6-* q7-*; do echo "== $d"; grep -vE '^$|unable to resolve|Could not chdir|bashrc' "$d/log.txt" | cut -c1-300; done
