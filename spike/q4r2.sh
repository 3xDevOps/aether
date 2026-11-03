# lock held, no rewrite: how long does CLI wait and what does it send
source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json; D=$SPIKE_HOME/.claude
mkcred $F fake-access-1 $(( $(now_ms) + 240000 )) fake-refresh-1
mkdir $D/.oauth_refresh.lock
cd /tmp/spike-work; date +%T; time (timeout 100 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1')
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token'; cat $F | head -c 150; echo
