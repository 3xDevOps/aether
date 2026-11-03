source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
export CC_EXTRA="CLAUDE_CODE_OAUTH_TOKEN=fake-env-1"
cd /tmp/spike-work
echo "--- 6a env token, no file, -p"; timeout 60 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1'; echo rc=$?; ls -a $SPIKE_HOME/.claude | tr '\n' ' '; echo
python3 /workspace/spike/show.py -f | cut -c1-330
python3 - <<'P'
import json
for l in open("/tmp/spike-run/mock.log"):
    r=json.loads(l)
    if r.get("path","").startswith("/v1/messages"): print(json.dumps(r["hdr"],indent=0)); break
P
rm -f $SPIKE_DIR/mock.log
echo '{"messages_status":401}' > $SPIKE_DIR/config.json
echo "--- 6b env token, 401"; timeout 60 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1'; echo rc=$?
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token' | cut -c1-200; ls -a $SPIKE_HOME/.claude | tr '\n' ' '; echo
