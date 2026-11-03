# q5c.sh <label> <expires-in-s> <with-refresh:0|1> [config]
source /workspace/spike/env.sh; source /workspace/spike/start.sh
export CC_EXTRA="CLAUDE_SECURESTORAGE_CONFIG_DIR=/tmp/spike-creds"
C=/tmp/spike-creds; chmod -R u+w $C 2>/dev/null; rm -rf $C; mkdir -p $C
F=$C/.credentials.json
if [ "$3" = 1 ]; then mkcred $F fake-access-1 $(( $(now_ms) + $2*1000 )) fake-refresh-1; else mkcred $F fake-access-1 $(( $(now_ms) + $2*1000 )); fi
chmod 444 $F; chmod 555 $C
[ -n "$4" ] && echo "$4" > $SPIKE_DIR/config.json
echo "== $1 (RO dir+file, creds dir via CLAUDE_SECURESTORAGE_CONFIG_DIR) expiresIn=$2 refresh=$3"
cd /tmp/spike-work; timeout 100 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1'; echo exit=$?
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token' | cut -c1-200; ls -la $C | tail -n +2; cat $F | head -c 250; echo; chmod -R u+w $C
