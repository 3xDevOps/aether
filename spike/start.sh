# start.sh : fresh run dir + mock; source it
source /workspace/spike/env.sh
[ -f /tmp/spike-mock.pid ] && kill $(cat /tmp/spike-mock.pid) 2>/dev/null
rm -rf $SPIKE_HOME $SPIKE_DIR; mkdir -p $SPIKE_HOME/.claude $SPIKE_DIR /tmp/spike-work
export GUARD_LOG=$SPIKE_DIR/guard.log
python3 /workspace/spike/mock.py >/dev/null 2>&1 & echo $! > /tmp/spike-mock.pid; disown; sleep 1
echo '{"hasCompletedOnboarding":true,"theme":"dark","projects":{"/tmp/spike-work":{"hasTrustDialogAccepted":true,"hasCompletedProjectOnboarding":true}}}' > $SPIKE_HOME/.claude/.claude.json
