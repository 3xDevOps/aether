source /workspace/spike/env.sh; source /workspace/spike/start.sh
export CC_EXTRA="CLAUDE_CODE_OAUTH_TOKEN=fake-env-1"
echo '{"messages_status":401}' > $SPIKE_DIR/config.json
cd /tmp/spike-work
echo "--- 6b env token, 401"; timeout 60 bash -c 'source /workspace/spike/env.sh; cc -p "hi" </dev/null 2>&1'; echo rc=$?
python3 /workspace/spike/show.py | grep -E 'messages|oauth/token' | cut -c1-120; ls -a $SPIKE_HOME/.claude | tr '\n' ' '; echo
