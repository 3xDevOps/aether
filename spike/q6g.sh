source /workspace/spike/env.sh; source /workspace/spike/start.sh
JWT=$(python3 - <<'P'
import base64,json,time
b=lambda o: base64.urlsafe_b64encode(json.dumps(o).encode()).rstrip(b"=").decode()
print(b({"alg":"none"})+"."+b({"sub":"u1","exp":int(time.time())+3600})+".sig")
P
)
export CC_EXTRA="CLAUDE_CODE_USE_GATEWAY=1 ANTHROPIC_AUTH_TOKEN=$JWT"
cd /tmp/spike-work
timeout 60 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1' | head; echo rc=$?
python3 - <<'P'
import json
for l in open("/tmp/spike-run/mock.log"):
    r=json.loads(l)
    if "path" in r: print(r["m"],r["host"],r["path"][:50],r["status"],{k:(v[:40]+"..." if k=="authorization" else v[:50]) for k,v in r["hdr"].items()})
P
ls -a $SPIKE_HOME/.claude | tr '\n' ' '
