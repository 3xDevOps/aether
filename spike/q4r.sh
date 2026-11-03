source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json; D=$SPIKE_HOME/.claude
mkcred $F fake-access-1 $(( $(now_ms) + 240000 )) fake-refresh-1
mkdir $D/.oauth_refresh.lock   # simulate another process holding the refresh lock (fresh mtime)
( sleep 4; python3 /workspace/spike/rewrite_inplace.py $F fake-access-X 7200 fake-refresh-X ) > $SPIKE_DIR/bg.log 2>&1 &
cd /tmp/spike-work; date +%T; time (timeout 100 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1'); 
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token'; cat $SPIKE_DIR/bg.log; ls -a $D | tr '\n' ' '; echo
