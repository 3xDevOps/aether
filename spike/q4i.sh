source /workspace/spike/env.sh; source /workspace/spike/start.sh
F=$SPIKE_HOME/.claude/.credentials.json
mkcred $F fake-access-1 $(( $(now_ms) + 7*60000 )) fake-refresh-1
echo "expiresAt = start+420s; refresh window opens at start+120s"
cd /tmp/spike-work
echo '[[10,"one\\r"],[130,""],[60,"two\\r"],[20,"three\\r"],[10,""]]' > /tmp/s4.json
CMD='source /workspace/spike/env.sh; cc' SETTLE=5 python3 /workspace/spike/drive.py /workspace/spike/runs/q4i.raw /tmp/s4.json
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token'; cat $F; echo
