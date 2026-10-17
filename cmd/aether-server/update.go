package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/serverupdate"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

const (
	updateUnit    = "aether-server"
	updateJournal = "sudo journalctl -u aether-server -n 50"
	// updateTimeout covers a new binary's whole trial: up to three starts
	// and the rollback after them.
	updateTimeout = 8 * time.Minute
)

// updateCmd moves this host to a release the way the server's own update
// does, then restarts the systemd unit onto it and waits for the new
// binary to commit or be rolled back.
func updateCmd(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("run it as root: sudo aether-server update")
	}
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	tag := fs.String("version", "", "release tag to install (default: the latest release)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: aether-server update [--version <tag>]")
	}
	// Before Lock, so a refused host gets nothing created under its data
	// directory with root's ownership.
	out, err := exec.Command("systemctl", "cat", updateUnit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("aether-server update restarts the %s systemd unit, and this host has none (systemctl cat %s: %w: %s); replace the binaries with `sudo aether update` and restart aether-server yourself",
			updateUnit, updateUnit, err, strings.TrimSpace(string(out)))
	}
	if user := unitProperty("User"); user != "" && user != "root" {
		return fmt.Errorf("the %s unit runs as %s: aether-server update needs the root unit that `aether-server install` writes; use `sudo aether update` and restart aether-server instead", updateUnit, user)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return fmt.Errorf("resolve this binary: %w", err)
	}
	// The trial runs in the process the unit starts, which reads the swap
	// record under its own data directory and runs its own binary, so the
	// update takes both from the unit.
	unitExe, o, err := unitServe()
	if err != nil {
		return err
	}
	if resolved, rerr := filepath.EvalSymlinks(unitExe); rerr != nil || resolved != self {
		return fmt.Errorf("the %s unit runs %s, not this binary (%s): run `sudo %s update`", updateUnit, unitExe, self, unitExe)
	}
	stateDir, database := server.UpdateStateDir(*o.dataDir), server.StorePath(*o.dataDir)
	unlock, err := serverupdate.Lock(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	ctx := context.Background()
	baseURL := selfupdate.DefaultChecker().BaseURL()
	target, err := updateTarget(ctx, baseURL, *tag)
	if err != nil {
		return err
	}
	if target == version.Version {
		return restartIfStale(self, *o.addr)
	}
	fmt.Printf("aether-server %s -> %s\n%s/releases/tag/%s\n", version.Version, target, baseURL, target)

	staged, err := serverupdate.Stage(ctx, serverupdate.Plan{
		BaseURL:  baseURL,
		Self:     self,
		StateDir: stateDir,
		Database: database,
		// Read without opening the store, so without migrating it.
		Schema: func(context.Context) (int, error) { return store.ReadSchemaVersion(database) },
		Now:    time.Now,
	}, target)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(probeCtx, self, "version").CombinedOutput(); err != nil {
		return errors.Join(fmt.Errorf("the new aether-server does not run: %w: %s", err, strings.TrimSpace(string(out))), staged.Undo())
	}
	before := unitPID()
	if err := restartOnto(staged); err != nil {
		return err
	}
	return awaitUpdate(stateDir, target, staged.Binaries, before)
}

// updateTarget is the tag to install: the one asked for, or the latest
// release when it is newer than this binary. A latest release no newer
// than this binary is the running version, which means up to date.
func updateTarget(ctx context.Context, baseURL, tag string) (string, error) {
	if tag != "" {
		if !selfupdate.ValidTag(tag) {
			return "", fmt.Errorf("%w (got %q)", serverupdate.ErrBadTag, tag)
		}
		return tag, nil
	}
	m, err := selfupdate.LatestManifestFrom(ctx, baseURL)
	if err != nil {
		return "", err
	}
	if !selfupdate.Behind(version.Version, m.Version) {
		return version.Version, nil
	}
	return m.Version, nil
}

// restartOnto stops the unit, snapshots the database once nothing writes
// to it, and starts the unit. A snapshot that fails starts the previous
// binary again instead. So does a start systemd refuses, which leaves no
// process to count the trial and roll it back.
func restartOnto(staged *serverupdate.Staged) error {
	if out, err := exec.Command("systemctl", "stop", updateUnit).CombinedOutput(); err != nil {
		return errors.Join(fmt.Errorf("systemctl stop %s: %w: %s", updateUnit, err, out), staged.Undo())
	}
	snapErr := staged.Snapshot()
	if snapErr != nil {
		snapErr = errors.Join(snapErr, staged.Undo())
	}
	out, err := exec.Command("systemctl", "start", updateUnit).CombinedOutput()
	if err == nil {
		return snapErr
	}
	startErr := fmt.Errorf("systemctl start %s: %w: %s", updateUnit, err, strings.TrimSpace(string(out)))
	if snapErr != nil {
		return errors.Join(snapErr, startErr)
	}
	// A process may have started, and migrated the database, before the
	// start failed: it is stopped before its files are put back.
	if out, err = exec.Command("systemctl", "stop", updateUnit).CombinedOutput(); err != nil {
		return errors.Join(startErr, fmt.Errorf("leaving the new binaries in place: systemctl stop %s: %w: %s", updateUnit, err, out))
	}
	if err = staged.RollBack(startErr.Error()); err != nil {
		return errors.Join(startErr, err)
	}
	if out, err = exec.Command("systemctl", "start", updateUnit).CombinedOutput(); err != nil {
		return errors.Join(startErr, fmt.Errorf("restored %s, and starting it failed too: systemctl start %s: %w: %s", version.Version, updateUnit, err, out))
	}
	return fmt.Errorf("%w; restored %s and started it\n%s", startErr, version.Version, updateJournal)
}

// awaitUpdate waits for the new binary to commit or be rolled back and
// prints one line per component.
func awaitUpdate(stateDir, target string, binaries []string, pidBefore string) error {
	deadline := time.Now().Add(updateTimeout)
	var out serverupdate.Outcome
	for {
		var err error
		if out, err = serverupdate.ReadOutcome(stateDir); err != nil {
			return err
		}
		if out.Settled {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not finish its first start within %s: unit %s, update record %q\n%s",
				target, updateTimeout, unitState(), out.Phase, updateJournal)
		}
		time.Sleep(time.Second)
	}
	if out.RolledBack {
		return fmt.Errorf("the update to %s was rolled back: %s\n%s", target, out.Error, updateJournal)
	}
	fmt.Printf("aether-server  %s -> %s  restarted (pid %s -> %s)\n", version.Version, target, pidBefore, unitPID())
	for _, path := range binaries[1:] {
		fmt.Printf("%-14s %s  %s\n", filepath.Base(path), path, target)
	}
	return nil
}

func unitProperty(name string) string {
	out, _ := exec.Command("systemctl", "show", "-p", name, "--value", updateUnit).Output()
	return strings.TrimSpace(string(out))
}

func unitPID() string { return unitProperty("MainPID") }

// unitServe is the executable and serve options the unit's ExecStart runs.
// systemctl prints ExecStart as `{ path=<exe> ; argv[]=<args> ; ... }`
// with the arguments joined by spaces.
func unitServe() (string, *serveOptions, error) {
	line := unitProperty("ExecStart")
	_, path, _ := strings.Cut(line, "path=")
	path, _, _ = strings.Cut(path, " ;")
	_, argv, _ := strings.Cut(line, "argv[]=")
	argv, _, _ = strings.Cut(argv, " ;")
	args := strings.Fields(argv)
	if path == "" || len(args) < 2 || args[1] != "serve" {
		return "", nil, fmt.Errorf("the %s unit's ExecStart does not run `aether-server serve`: %q", updateUnit, line)
	}
	o, _, rest, err := loadOptions("serve", args[2:], nil)
	if err != nil {
		return "", nil, fmt.Errorf("read the %s unit's serve options %q: %w", updateUnit, argv, err)
	}
	if len(rest) != 0 {
		return "", nil, fmt.Errorf("the %s unit's ExecStart has arguments serve does not take: %q", updateUnit, argv)
	}
	return path, o, nil
}

// restartIfStale restarts the unit when it still runs an earlier file than
// the binary installed at self, as after `aether update` replaced it, and
// starts it when it runs nothing. The unit is Type=simple, so a new PID
// only means the process started; it serves once it accepts connections on
// addr.
func restartIfStale(self, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	dial := net.JoinHostPort(host, port)
	pid := unitPID()
	installed, err := os.Stat(self)
	if err != nil {
		return err
	}
	verb := "start"
	if pid != "" && pid != "0" {
		running, serr := os.Stat("/proc/" + pid + "/exe")
		if serr != nil {
			return fmt.Errorf("read the binary %s pid %s runs: %w", updateUnit, pid, serr)
		}
		if os.SameFile(installed, running) {
			fmt.Printf("aether-server %s is up to date\n", version.Version)
			return nil
		}
		verb = "restart"
	}
	if out, rerr := exec.Command("systemctl", verb, updateUnit).CombinedOutput(); rerr != nil {
		return fmt.Errorf("systemctl %s %s: %w: %s", verb, updateUnit, rerr, out)
	}
	deadline := time.Now().Add(2 * time.Minute)
	started := ""
	for time.Now().Before(deadline) {
		now, state := unitPID(), unitState()
		switch {
		case started == "" && now != pid && now != "0" && state == "active":
			started = now
		case started != "" && (now != started || state != "active"):
			return fmt.Errorf("%s pid %s stopped after the %s: unit %s, main pid now %s\n%s", updateUnit, started, verb, state, now, updateJournal)
		}
		if started != "" {
			if conn, derr := net.DialTimeout("tcp", dial, time.Second); derr == nil {
				_ = conn.Close()
				if verb == "start" {
					fmt.Printf("aether-server  %s  started (pid %s)\n", version.Version, started)
				} else {
					fmt.Printf("aether-server  %s  restarted onto the installed binary (pid %s -> %s)\n", version.Version, pid, started)
				}
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("%s did not accept connections on %s within 2 minutes of the %s: unit %s\n%s", updateUnit, dial, verb, unitState(), updateJournal)
}

func unitState() string {
	out, _ := exec.Command("systemctl", "is-active", updateUnit).Output()
	return strings.TrimSpace(string(out))
}
