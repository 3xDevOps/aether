package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/runtime"
)

type unitState struct {
	Active  bool
	MainPID int
}

// units runs each VM's monitor in its own transient systemd unit, outside
// the server's process tree, so a VM outlives a server restart or update.
type units interface {
	Start(ctx context.Context, name string, argv, properties []string) error
	Stop(ctx context.Context, name string) error
	State(ctx context.Context, name string) (unitState, error)
}

func unitName(key string) string { return "aether-vm-" + key + ".service" }

type systemdUnits struct{}

// Start uses --collect so a unit that failed does not keep its name from
// the next Start.
func (systemdUnits) Start(ctx context.Context, name string, argv, properties []string) error {
	args := []string{"--unit=" + name, "--slice=" + runtime.SessionSlice, "--collect", "--quiet"}
	for _, p := range properties {
		args = append(args, "--property="+p)
	}
	args = append(append(args, "--"), argv...)
	return systemctl(ctx, "systemd-run", args...)
}

// Stop treats a unit systemd no longer knows as stopped.
func (systemdUnits) Stop(ctx context.Context, name string) error {
	err := systemctl(ctx, "systemctl", "stop", name)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 5 {
		return nil
	}
	return err
}

// State counts a unit as active while it starts, runs or reloads, so a VM
// whose monitor is still composing its share is not mistaken for gone.
func (systemdUnits) State(ctx context.Context, name string) (unitState, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState,MainPID", name).Output()
	if err != nil {
		return unitState{}, fmt.Errorf("vm runtime: systemctl show %s: %w", name, err)
	}
	var st unitState
	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ActiveState":
			st.Active = value == "active" || value == "activating" || value == "reloading"
		case "MainPID":
			st.MainPID, _ = strconv.Atoi(value)
		}
	}
	return st, nil
}

func systemctl(ctx context.Context, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vm runtime: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
