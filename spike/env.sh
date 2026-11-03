# fresh environment for the CLI under test; source then call `cc ...`
export SPIKE_DIR=${SPIKE_DIR:-/tmp/spike-run}
export SPIKE_HOME=${SPIKE_HOME:-/tmp/spike-home}
S=/workspace/spike
CLAUDE_BIN=/tmp/spike-npm/node_modules/@anthropic-ai/claude-code/bin/claude.exe
cc() { env -i PATH=/usr/bin:/bin HOME=$SPIKE_HOME CLAUDE_CONFIG_DIR=$SPIKE_HOME/.claude TMPDIR=/tmp \
  ANTHROPIC_BASE_URL=http://127.0.0.1:8801 HTTPS_PROXY=http://127.0.0.1:8802 NO_PROXY=127.0.0.1,localhost NODE_EXTRA_CA_CERTS=$S/certs/ca.pem \
  CLAUDE_CODE_DISABLE_AUTOUPDATE=1 DISABLE_AUTOUPDATER=1 $CC_EXTRA python3 $S/guard.py "$CLAUDE_BIN" "$@"; }
mkcred() { # mkcred <file> <access> <expiresAt-ms> [refresh]
  python3 - "$@" <<'P'
import json,sys
f,a,e=sys.argv[1:4]; o={"accessToken":a,"expiresAt":int(e),"scopes":["user:inference","user:profile"],"subscriptionType":"max"}
if len(sys.argv)>4: o["refreshToken"]=sys.argv[4]
open(f,"w").write(json.dumps({"claudeAiOauth":o}))
P
}
now_ms() { python3 -c 'import time;print(int(time.time()*1000))'; }
