source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
mkcred $F fake-access-1 $(( $(now_ms) + 8*3600000 ))
( sleep 14; python3 /workspace/spike/rewrite_keep_mtime.py $F fake-access-2 28800; echo '{"messages_status_by_token":{"fake-access-1":401}}' > $SPIKE_DIR/config.json; sleep 40; echo '{"messages_status":401}' > $SPIKE_DIR/config.json ) > $SPIKE_DIR/rewrites.log 2>&1 &
cd /tmp/spike-work
echo '[[10,"a\\r"],[20,"b\\r"],[40,"c\\r"],[20,""]]' > /tmp/s3.json
CMD='source /workspace/spike/env.sh; cc' SETTLE=5 python3 /workspace/spike/drive.py /workspace/spike/runs/q3b.raw /tmp/s3.json
cat $SPIKE_DIR/rewrites.log; python3 /workspace/spike/show.py | grep -E 'messages|oauth/token'; ls -a $SPIKE_HOME/.claude | tr '\n' ' '; cat $F; echo
python3 /workspace/spike/strip.py /workspace/spike/runs/q3b.raw 3000
