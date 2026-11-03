source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
mkcred $F fake-access-1 $(( $(now_ms) + 240000 )) fake-refresh-1
export CC_EXTRA="CLAUDE_CODE_CUSTOM_OAUTH_URL=http://127.0.0.1:8801"
cd /tmp/spike-work
echo "--- CUSTOM_OAUTH_URL=http://127.0.0.1:8801 (-p)"; timeout 60 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1' | head -8; echo rc=$?
python3 /workspace/spike/show.py -a | grep -v -E 'event_logging|proxy_connect' | cut -c1-200
