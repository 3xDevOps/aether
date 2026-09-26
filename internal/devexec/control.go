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

// ReapTimeout bounds cleanup after SIGKILL. An unkillable descendant makes the
// execution unavailable; it must never be reported as successfully stopped.
const ReapTimeout = 5 * time.Second

// State is written before the helper exits. An exited Docker exec without a
// matching final record is unavailable, not an invented successful command.
type State struct {
	CreationKey string `json:"creation_key"`
	ExecID      string `json:"exec_id"`
	ClaimToken  string `json:"claim_token"`
	Running     bool   `json:"running"`
	Exited      bool   `json:"exited"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
}

type Request struct {
	Action      string `json:"action"`
	ExecID      string `json:"exec_id"`
	ClaimToken  string `json:"claim_token"`
	GraceMillis int64  `json:"grace_millis,omitempty"`
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
	defer func() { _ = f.Close() }()
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
	if key == "" || len(key) > 1024 || request.ExecID == "" || request.ClaimToken == "" {
		return State{}, errors.New("execution, creation and claim identities are required")
	}
	switch request.Action {
	case "start", "status":
	case "stop":
		if request.GraceMillis < 0 || request.GraceMillis > 60000 {
			return State{}, errors.New("stop grace must be between 0 and 60000 milliseconds")
		}
	default:
		return State{}, errors.New("unknown execution control operation")
	}
	ctx, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	for {
		if state, err := readState(key); err == nil && state.Exited {
			if state.ExecID != request.ExecID || state.ClaimToken != request.ClaimToken {
				return State{}, errors.New("execution identity mismatch")
			}
			if request.Action == "start" {
				return state, errors.New("execution creation key has already been used")
			}
			return state, nil
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(stateDir(key), "control.sock"))
		if err == nil {
			defer func() { _ = conn.Close() }()
			deadline, _ := ctx.Deadline()
			if deadlineErr := conn.SetDeadline(deadline); deadlineErr != nil {
				return State{}, deadlineErr
			}
			// A deadline alone does not unblock an already connected socket
			// when its parent context is cancelled before that deadline.
			stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stopCancel()
			if encodeErr := json.NewEncoder(conn).Encode(request); encodeErr != nil {
				return State{}, encodeErr
			}
			var state State
			err = json.NewDecoder(io.LimitReader(conn, 16<<10)).Decode(&state)
			if err != nil {
				if ctx.Err() != nil {
					return State{}, ctx.Err()
				}
				return State{}, err
			}
			if state.CreationKey != key || state.ExecID != request.ExecID || state.ClaimToken != request.ClaimToken {
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
