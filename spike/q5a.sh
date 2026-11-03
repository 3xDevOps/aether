source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
mkcred $F fake-access-1 $(( $(now_ms) + 8*3600000 )) fake-refresh-1
cd /tmp/spike-work
echo "--- auth status"; timeout 60 bash -c 'source /workspace/spike/env.sh; cc auth status </dev/null 2>&1' | head -20; stat -c 'inode=%i mtime=%y' $F
echo "--- auth logout"; timeout 60 bash -c 'source /workspace/spike/env.sh; cc auth logout </dev/null 2>&1'; echo rc=$?; ls -la $SPIKE_HOME/.claude/.credentials.json 2>&1; cat $F 2>/dev/null
python3 /workspace/spike/show.py -a | grep -v -E 'event_logging|proxy_connect'
