// Package devexec implements the in-container side of owned development PTYs.
// The version-matched staged server binary runs it; user images need no helper
// utilities beyond the command they want to execute.
package devexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const Command = "dev-exec"
const StartupTimeout = 15 * time.Second

// State is written before the helper exits. An exited Docker exec without a
// matching final record is unavailable, not an invented successful command.
type State struct {
	CreationKey string `json:"creation_key"`
	ExecID string `json:"exec_id"`
	Running bool `json:"running"`
	Exited bool `json:"exited"`
	ExitCode *int `json:"exit_code,omitempty"`
	Error string `json:"error,omitempty"`
}

type Request struct {
	Action string `json:"action"`
	ExecID string `json:"exec_id"`
	GraceMillis int64 `json:"grace_millis,omitempty"`
}

func stateDir(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join("/tmp", ".aether-devexec-"+hex.EncodeToString(sum[:20]))
}

func readState(key string) (State, error) {
	var state State
	f, err := os.Open(filepath.Join(stateDir(key), "state.json"))
	if err != nil {
		return state, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 16<<10)).Decode(&state)
	if err == nil && state.CreationKey != key {
		err = errors.New("execution creation identity mismatch")
	}
	return state, err
}

// Control talks to the owning supervisor, never a recorded numeric PID. The
// deadline bounds startup waiting as well as the request itself. Completed
// state can still be read after the owning supervisor has exited.
func Control(ctx context.Context, key string, request Request) (State, error) {
	if key == "" || request.ExecID == "" {
		return State{}, errors.New("execution and creation identities are required")
	}
	ctx, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	for {
		if state, err := readState(key); err == nil && state.Exited {
			if state.ExecID != request.ExecID {
				return State{}, errors.New("execution identity mismatch")
			}
			return state, nil
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(stateDir(key), "control.sock"))
		if err == nil {
			defer conn.Close()
			deadline, _ := ctx.Deadline()
			if err := conn.SetDeadline(deadline); err != nil {
				return State{}, err
			}
			if err := json.NewEncoder(conn).Encode(request); err != nil {
				return State{}, err
			}
			var state State
			err = json.NewDecoder(io.LimitReader(conn, 16<<10)).Decode(&state)
			if err != nil {
				return State{}, err
			}
			if state.CreationKey != key || state.ExecID != request.ExecID {
				return State{}, errors.New("execution identity mismatch")
			}
			if state.Error != "" {
				return state, errors.New(state.Error)
			}
			return state, nil
		}
		if request.Action != "start" && request.Action != "stop" {
			return State{}, fmt.Errorf("execution control unavailable: %w", err)
		}
		select {
		case <-ctx.Done():
			return State{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func saveState(state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	name := filepath.Join(stateDir(state.CreationKey), "state.json")
	if err := os.WriteFile(name+".new", data, 0o600); err != nil {
		return err
	}
	return os.Rename(name+".new", name)
}
