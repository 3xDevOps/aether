# q4.sh <label> <expires-in-seconds> [config-json] [extra-bg-cmd]
source /workspace/spike/env.sh; source /workspace/spike/start.sh
L=$1; F=$SPIKE_HOME/.claude/.credentials.json; D=$SPIKE_HOME/.claude
mkcred $F fake-access-1 $(( $(now_ms) + $2*1000 )) fake-refresh-1
[ -n "$3" ] && echo "$3" > $SPIKE_DIR/config.json
echo "== $L: expiresIn=${2}s cfg=$3"; stat -c 'before inode=%i mtime=%y' $F
( for i in $(seq 1 60); do ls -ad $D/.oauth_refresh.lock* $D/*.lock 2>/dev/null | sed "s/^/lock-seen t=$i: /"; sleep 0.25; done ) > $SPIKE_DIR/lockwatch.log 2>&1 &
[ -n "$4" ] && ( eval "$4" ) > $SPIKE_DIR/bg.log 2>&1 &
cd /tmp/spike-work; timeout 100 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1'; echo exit=$?
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token'; stat -c 'after  inode=%i mtime=%y' $F; cat $F; echo; sort -u $SPIKE_DIR/lockwatch.log | head -3; cat $SPIKE_DIR/bg.log 2>/dev/null; ls -a $D | tr '\n' ' '; echo
