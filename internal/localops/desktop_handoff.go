package localops

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Windows cannot rename an installed Electron app while it runs, and the
// gateway that rebuilds it is a child of that app. update.apply therefore
// stages the new app first (`aether gui build --stage`), starts this helper
// detached (`aether gui build --staged --after-exit <pids>`), and quits the
// app. The helper outlives all of it: it waits for the app and the gateway
// to exit, swaps the staged app in, and starts an app again.

const (
	// StagedAppDir is the directory under the build directory that
	// `gui build --stage` fills and `gui build --staged` installs from.
	StagedAppDir = "staged-app"

	// handoffTimeout bounds the wait for the app to exit and the swap
	// retries after it, so a helper never lingers.
	handoffTimeout = time.Minute

	// handoffRetry is how often a swap that failed is retried: an Electron
	// child process can hold a file in the app directory for a moment
	// after the main process has exited.
	handoffRetry = 250 * time.Millisecond

	daemonTask = "aether-daemon"
)

// StartDesktopHandoff starts the helper from bin, the updated CLI, for the
// app and gateway processes in pids. It returns once the helper is running.
func StartDesktopHandoff(bin string, pids []int) error {
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	cmd := exec.Command(bin, "gui", "build", "--staged", "--after-exit", strings.Join(list, ","))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("localops: start the desktop update helper: %w", err)
	}
	return cmd.Process.Release()
}

// ParseExitPIDs reads the --after-exit list.
func ParseExitPIDs(list string) ([]int, error) {
	var pids []int
	for _, field := range strings.Split(list, ",") {
		if field == "" {
			continue
		}
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("localops: %q is not a process id", field)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// handoffOps is what the helper does to the machine, injected so the order
// and the failure paths run in a test.
type handoffOps struct {
	waitExit  func(pid int, timeout time.Duration) error
	endDaemon func()
	install   func() error
	launch    func() error
}

// FinishDesktopUpdate waits for pids to exit, ends the sync daemon's
// Scheduled Task, runs install, and starts the app at appDir. When any step
// before the swap fails, or the swap does, the app still on disk is started
// instead and the reason is recorded for the next dashboard
// (RecordDesktopBuildError). install replaces the app with rollback, as
// InstallDesktop does.
func FinishDesktopUpdate(appDir string, pids []int, install func() error) error {
	exe := filepath.Join(appDir, desktopExe)
	return finishDesktopUpdate(handoffOps{
		waitExit:  waitForExit,
		endDaemon: endDaemonTask,
		install:   install,
		launch: func() error {
			cmd := exec.Command(exe)
			cmd.Dir = appDir
			detach(cmd)
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Process.Release()
		},
	}, pids, handoffTimeout, handoffRetry)
}

func finishDesktopUpdate(ops handoffOps, pids []int, timeout, retry time.Duration) error {
	deadline := time.Now().Add(timeout)
	var err error
	for _, pid := range pids {
		if err = ops.waitExit(pid, time.Until(deadline)); err != nil {
			err = fmt.Errorf("wait for process %d to exit: %w", pid, err)
			break
		}
	}
	if err == nil {
		ops.endDaemon()
		for {
			if err = ops.install(); err == nil || time.Until(deadline) < retry {
				break
			}
			time.Sleep(retry)
		}
	}
	if err != nil {
		err = fmt.Errorf("localops: the desktop app was not updated: %w", err)
		if recordErr := RecordDesktopBuildError(err.Error()); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
	}
	if launchErr := ops.launch(); launchErr != nil {
		err = errors.Join(err, fmt.Errorf("localops: start the desktop app: %w", launchErr))
	}
	return err
}

// endDaemonTask ends the sync daemon's Scheduled Task, as scripts/install.ps1
// asks the user to: the task restarts a stopped daemon within a minute, and
// the daemon runs the CLI that was just replaced. A task that does not
// exist or is not running makes schtasks fail, which leaves nothing to end.
func endDaemonTask() {
	if exec.Command("schtasks", "/Query", "/TN", daemonTask).Run() != nil {
		return
	}
	_ = exec.Command("schtasks", "/End", "/TN", daemonTask).Run()
}
