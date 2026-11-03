source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
mkcred $F fake-access-1 $(( $(now_ms) + 150000 ))
stat -c 'start inode=%i mtime=%y' $F
RW="python3 /workspace/spike/rewrite_inplace.py $F"
( sleep 40; $RW fake-access-2 7200;  sleep 40; $RW fake-access-3 80;  sleep 140; $RW fake-access-4 7200 ) > $SPIKE_DIR/rewrites.log 2>&1 &
cd /tmp/spike-work
# steps (delays are relative to the previous step): msg@10 one(tok1) | @50 two (after rewrite→tok2) | @100 three (after rewrite→tok3,80s life) | @190 four (tok3 expired, no rewrite yet) | @250 five (after rewrite→tok4)
echo '[[10,"one\\r"],[40,"two\\r"],[50,"three\\r"],[90,"four\\r"],[60,"five\\r"],[10,""]]' > /tmp/s2.json
CMD='source /workspace/spike/env.sh; cc' SETTLE=10 python3 /workspace/spike/drive.py /workspace/spike/runs/q2.raw /tmp/s2.json
stat -c 'end inode=%i mtime=%y' $F
