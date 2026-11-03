source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
mkcred $F fake-access-1 $(( $(now_ms) + 8*3600000 ))
echo '{"messages_status":401}' > $SPIKE_DIR/config.json
cd /tmp/spike-work; time (timeout 110 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1'; echo exit=$?)
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token'; ls -a $SPIKE_HOME/.claude; stat -c '%i %y' $F; cat $F | head -c 200
