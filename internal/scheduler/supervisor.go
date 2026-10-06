package scheduler

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

// supervisorStateFile is where the run supervisor records the child a mode
// switch started. It is inside the container because the run directory is
// read-only there.
const supervisorStateFile = "/tmp/aether-supervisor"

// wrapTUICommand runs the harness as the first child of a POSIX-shell
// supervisor. Harness arguments stay positional parameters so argv can never
// become shell source. After the harness exits, a login shell keeps the
// container available until the run is closed or killed; with no argv (an
// ACP-driven run) the login shell is the first child. SIGUSR1 and SIGUSR2
// end the container with 0 and 1, which is how a background run over ACP
// reports its one turn's outcome. SIGALRM swaps the child for a mode switch
// (swapChild).
func wrapTUICommand(argv []string) []string {
	return supervisorCommand(path.Join(coordtransport.MountDir, coordtransport.NextCommandName), supervisorStateFile, argv)
}

func supervisorCommand(nextFile, stateFile string, argv []string) []string {
	script := "next_file=" + shellquote.Quote(nextFile) + "\nstate_file=" + shellquote.Quote(stateFile) + "\n" + supervisorScript
	return append([]string{"/bin/sh", "-c", script, "aether-run-supervisor"}, argv...)
}

// supervisorScript ends the current child on SIGALRM and then runs the body
// of the next-command file, or a login shell when the body is empty. A
// second SIGALRM while the first is pending kills the child. The file's
// first line is "# <nonce>"; the state file reads "<nonce> started" once the
// old child is gone and "<nonce> exited <status>" when the new one exits.
const supervisorScript = `exec 3<&0
nl='
'
child=
child_signal=TERM
child_signaled=
pending_signal=
pending_status=
swap=
interrupted=

forward_shutdown() {
	if [ -n "$child" ] && [ -z "$child_signaled" ]; then
		kill -"$child_signal" "$child" 2>/dev/null || :
		child_signaled=1
	fi
}

request_shutdown() {
	interrupted=1
	if [ -z "$pending_signal" ]; then
		pending_signal=$1
		pending_status=$2
	fi
	forward_shutdown
}

request_swap() {
	interrupted=1
	if [ -n "$swap" ] && [ -n "$child" ]; then
		kill -KILL "$child" 2>/dev/null || :
		return
	fi
	swap=1
	if [ -n "$child" ]; then
		kill -"$child_signal" "$child" 2>/dev/null || :
	fi
}

trap 'request_shutdown TERM 143' TERM
trap 'request_shutdown INT 130' INT
trap 'request_shutdown HUP 129' HUP
trap 'request_shutdown USR1 0' USR1
trap 'request_shutdown USR2 1' USR2
trap request_swap ALRM

run_child() {
	child_signal=$1
	shift
	child_signaled=
	if [ -n "$pending_signal" ]; then
		exit "$pending_status"
	fi
	"$@" <&3 &
	child=$!
	if [ -n "$pending_signal" ]; then
		forward_shutdown
	elif [ -n "$swap" ]; then
		kill -"$child_signal" "$child" 2>/dev/null || :
	fi
	while :
	do
		interrupted=
		wait "$child" 2>/dev/null
		status=$?
		[ -n "$interrupted" ] || break
	done
	if [ -n "$pending_signal" ]; then
		exit "$pending_status"
	fi
	child=
	child_signaled=
	return "$status"
}

if [ "$#" -gt 0 ]; then
	run_child TERM "$@"
	status=$?
	if [ -z "$swap" ]; then
		printf '\n[aether] harness exited with code %s\n' "$status"
	fi
fi
while :
do
	if [ -n "$pending_signal" ]; then
		exit "$pending_status"
	fi
	body=
	if [ -n "$swap" ]; then
		swap=
		next=$(cat "$next_file" 2>/dev/null) || next=
		nonce=${next%%"$nl"*}
		nonce=${nonce#"# "}
		case $next in
		*"$nl"*) body=${next#*"$nl"} ;;
		esac
		printf '%s started\n' "$nonce" > "$state_file"
	fi
	if [ -n "$body" ]; then
		run_child TERM /bin/sh -c "$body"
		status=$?
		if [ -z "$swap" ]; then
			printf '%s exited %s\n' "$nonce" "$status" > "$state_file"
			printf '\n[aether] harness exited with code %s\n' "$status"
		fi
	elif [ -x /bin/bash ]; then
		run_child HUP /bin/bash -l
	else
		run_child HUP /bin/sh -l
	fi
done`

// swapScript asks the supervisor to swap its child and waits for the old one
// to be gone: SIGALRM, a second SIGALRM after 10 seconds, failure after 15.
// It then waits $2 seconds and fails with exit 3 and the new child's status
// on stdout if that child has already exited. PID 1 is Docker's init, which
// forwards the signal to the supervisor.
const swapScript = `nonce=$1 settle=$2 state=$3
kill -ALRM 1 || exit 1
i=0
while :
do
	case $(cat "$state" 2>/dev/null) in
	"$nonce "*) break ;;
	esac
	i=$((i + 1))
	if [ "$i" -eq 100 ]; then
		kill -ALRM 1
	fi
	if [ "$i" -ge 150 ]; then
		echo "the run supervisor did not stop its child within 15 seconds" >&2
		exit 2
	fi
	sleep 0.1
done
sleep "$settle"
s=$(cat "$state")
case $s in
"$nonce exited "*)
	echo "${s##* }"
	exit 3
	;;
esac`

// swapBound bounds the swap exec: the script's own 15 seconds, the settle
// time and Docker's exec round trips.
const swapBound = 30 * time.Second

// swapChild has the supervisor of container cid run the next-command file
// carrying nonce in place of its current child. With settle, the new child
// must still be running that long after it started.
func (s *Scheduler) swapChild(ctx context.Context, cid runtime.ID, nonce string, settle time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, swapBound)
	defer cancel()
	argv := []string{"/bin/sh", "-c", swapScript, "aether-swap", nonce, strconv.Itoa(int(settle / time.Second)), supervisorStateFile}
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, cid, argv, "")
	switch {
	case err != nil:
		return fmt.Errorf("signal the run supervisor: %w", err)
	case code == 3:
		return fmt.Errorf("the agent's terminal exited with code %s as it started; the Terminal tab shows its output", strings.TrimSpace(stdout))
	case code != 0:
		return fmt.Errorf("signal the run supervisor: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}
