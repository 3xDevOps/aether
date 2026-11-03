source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
mkcred $F fake-access-1 $(( $(now_ms) + 8*3600000 ))
cd /tmp/spike-work
timeout 60 bash -c 'source /workspace/spike/env.sh; cc auth logout </dev/null 2>&1'; echo rc=$?; ls -la $F 2>&1
python3 /workspace/spike/show.py -a | grep -v -E 'event_logging|proxy_connect'
grep -h revoke $SPIKE_DIR/mock.log | cut -c1-400
