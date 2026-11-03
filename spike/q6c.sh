source /workspace/spike/env.sh; source /workspace/spike/start.sh
echo fake-helper-1 > $SPIKE_DIR/helper.token
echo '{"apiKeyHelper":"/tmp/helper.sh"}' > $SPIKE_HOME/.claude/settings.json
export CC_EXTRA="CLAUDE_CODE_API_KEY_HELPER_TTL_MS=15000"
cd /tmp/spike-work
echo "--- 6c apiKeyHelper, TTL 15s, no creds file; msgs at 10s,20s(after TTL),then 401 only for helper-1 and token swapped"
# at +32s: swap helper token to helper-2 and make mock 401 helper-1 ; msg at +45
( sleep 32; echo fake-helper-2 > $SPIKE_DIR/helper.token; echo '{"messages_status_by_token":{"fake-helper-1":401}}' > $SPIKE_DIR/config.json ) &
echo '[[10,"one\\r"],[12,"two\\r"],[25,"three\\r"],[15,""]]' > /tmp/s6.json
CMD='source /workspace/spike/env.sh; cc' SETTLE=5 python3 /workspace/spike/drive.py /workspace/spike/runs/q6c.raw /tmp/s6.json
echo "helper invocations:"; cat $SPIKE_DIR/helper.log
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token|profile' | cut -c1-160
python3 - <<'P'
import json
for l in open("/tmp/spike-run/mock.log"):
    r=json.loads(l)
    if r.get("path","").startswith("/v1/messages"): print(json.dumps(r["hdr"])[:600]); break
P
ls -a $SPIKE_HOME/.claude | tr '\n' ' '
