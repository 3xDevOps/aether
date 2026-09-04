// Command coordagent is the coordination E2E's in-container agent. It is
// built and installed as the image's "claude" executable, so the shipped
// claude profile launches it verbatim - flags, task, and the appended
// --mcp-config - and everything it reports it learned from inside a real
// run container as the image's non-root user.
//
// It knows nothing about Aether's own paths: the coordination directory is
// the directory of the config it was pointed at, the bridge command is what
// that config names, and the socket is whatever socket the directory holds.
// A real harness has exactly that much.
//
// It never exits. Its whole conversation with the test is its terminal, and
// a run whose agent exits is a run the test can no longer attach to.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// The three tools, as an agent sees them.
const (
	toolStatus = "aether_status"
	toolSend   = "aether_send"
	toolInbox  = "aether_inbox"
)

// shared is the file every coordination run edits, which is what puts the
// runs in the conflict radar's overlap set.
const shared = "shared.txt"

// poll bounds the waits on the radar and on the inbox.
const poll = 2 * time.Minute

// retouch is how often the shared file is rewritten while the agent waits
// for a peer. The diff watch snapshots on quiescence - a couple of seconds
// after the last change, and never more often than its own floor - so
// rewriting on every poll would be sustained churn that holds off the very
// snapshot the radar is waiting for. One write, then quiet.
const retouch = 10 * time.Second

func main() {
	// The overlap notice arrives on stdin and the write carrying it blocks
	// until it is read, so drain for the whole life of the run.
	go drainStdin()
	// The supervisor attaches this terminal just after the container
	// starts, and output the terminal never carried is output no test can
	// read. Waiting is only ever a head start, never a guarantee: what has
	// to survive a miss - the edit the conflict radar watches for - is
	// repeated below rather than said once here.
	time.Sleep(2 * time.Second)
	say("argv:%s", strings.Join(os.Args, " "))
	say("user:%d:%d", os.Getuid(), os.Getgid())
	if err := coordinate(context.Background()); err != nil {
		say("agent-error:%s", err)
	}
	// Never exit: the test reads this terminal long after the round trip,
	// and a run whose agent exits is a run it can no longer attach to.
	for {
		time.Sleep(time.Hour)
	}
}

func coordinate(ctx context.Context) error {
	configPath := flagValue("--mcp-config")
	if configPath == "" {
		say("assets:notice-only")
		return nil
	}
	dir := filepath.Dir(configPath)
	// Traversing the coordination directory, reading the config out of it,
	// and being refused a write to it are all the non-root user's own.
	if err := reportDir(dir); err != nil {
		return err
	}
	if err := reportMount(dir); err != nil {
		return err
	}
	if err := reportReadOnly(dir); err != nil {
		return err
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	command, args, err := bridgeCommand(raw)
	if err != nil {
		return err
	}
	if err := reportMode(command); err != nil {
		return err
	}
	if err := reportMount(command); err != nil {
		return err
	}

	say("bridge:%s %s", command, strings.Join(args, " "))
	bridge := exec.CommandContext(ctx, command, args...)
	bridge.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "coordagent", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: bridge}, nil)
	if err != nil {
		return fmt.Errorf("connect to the bridge: %w", err)
	}
	defer cs.Close() //nolint:errcheck // the agent never returns from main

	status, peer, err := waitPeer(ctx, cs)
	if err != nil {
		return err
	}
	say("status:%s wire=%s", status.RunID, status.WireVersion)
	say("peer:%s", peer.RunID)

	var sent protocol.CoordSendResult
	body := "handled by " + status.RunID
	if err := call(ctx, cs, toolSend, protocol.CoordSendParams{ToRunID: peer.RunID, Body: body}, &sent); err != nil {
		return err
	}
	say("sent:%s", sent.MessageID)

	msg, err := waitInbox(ctx, cs)
	if err != nil {
		return err
	}
	say("inbox:%s", msg.Body)
	return nil
}

// flagValue returns the value of a "--flag value" pair in the argv the
// harness was launched with.
func flagValue(name string) string {
	for i, arg := range os.Args {
		if arg == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

// bridgeCommand reads the stdio server the MCP config names.
func bridgeCommand(raw []byte) (string, []string, error) {
	var doc struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmt.Errorf("decode mcp config: %w", err)
	}
	for _, server := range doc.Servers {
		if server.Type == "stdio" && server.Command != "" {
			return server.Command, server.Args, nil
		}
	}
	return "", nil, fmt.Errorf("mcp config names no stdio server: %s", raw)
}

// reportDir lists the coordination directory with the mode of every entry,
// which is the permission contract the container half rests on.
func reportDir(dir string) error {
	if err := reportMode(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	modes := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, ierr := entry.Info()
		if ierr != nil {
			return fmt.Errorf("stat %s: %w", filepath.Join(dir, entry.Name()), ierr)
		}
		modes = append(modes, fmt.Sprintf("%s=%04o", entry.Name(), info.Mode().Perm()))
	}
	slices.Sort(modes)
	say("entries:%s", strings.Join(modes, " "))
	return nil
}

func reportMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	say("mode:%s=%04o", path, info.Mode().Perm())
	return nil
}

// reportMount proves the target is a mount of its own and a read-only one,
// out of the kernel's own record of it. This is the fact the container half
// rests on, and the only form of it a mode bit cannot imitate.
func reportMount(target string) error {
	options, err := mountOptions(target)
	if err != nil {
		return err
	}
	if !slices.Contains(options, "ro") {
		return fmt.Errorf("%s is mounted %s, want ro", target, strings.Join(options, ","))
	}
	say("mount:%s=ro", target)
	return nil
}

// mountOptions returns the per-mount options the kernel records for target
// in /proc/self/mountinfo: id, parent, device, root, mount point, options.
// A path that is not a mount point of its own has no line there at all,
// which is the other half of what reportMount proves.
func mountOptions(target string) ([]string, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if fields := strings.Fields(line); len(fields) >= 6 && fields[4] == target {
			return strings.Split(fields[5], ","), nil
		}
	}
	return nil, fmt.Errorf("%s is not a mount point", target)
}

// reportReadOnly is the same refusal an agent would actually run into, and
// it has to be EROFS: an EACCES would say nothing about how the path was
// mounted. Only the coordination directory is probed this way. The staged
// binary is 0555, so a non-root write to it is refused either way and the
// mount record above is the only thing that can tell the two apart.
func reportReadOnly(dir string) error {
	probe := filepath.Join(dir, "agent-probe")
	err := os.WriteFile(probe, []byte("probe\n"), 0o600)
	if err == nil {
		_ = os.Remove(probe)
	}
	if !errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("write to %s: %v, want %v", probe, err, syscall.EROFS)
	}
	say("readonly:%s", dir)
	return nil
}

// touchShared rewrites the file every coordination run edits, which is what
// puts the runs in the conflict radar's overlap set.
func touchShared() error {
	if err := os.WriteFile(shared, []byte("edited by "+os.Getenv("AETHER_RUN_ID")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", shared, err)
	}
	return nil
}

// waitPeer polls aether_status until the radar authorizes a peer.
func waitPeer(ctx context.Context, cs *mcp.ClientSession) (protocol.CoordStatusResult, protocol.CoordPeer, error) {
	deadline := time.Now().Add(poll)
	var touched time.Time
	for {
		// The diff watch is fsnotify-driven and registered just after the
		// container is attached, so an edit made before it exists is an
		// edit the radar never hears about - and the run then waits out
		// the poll for a peer that will never be authorized. Rewriting the
		// file makes a missed event cost one interval, not the scenario.
		if time.Since(touched) >= retouch {
			if err := touchShared(); err != nil {
				return protocol.CoordStatusResult{}, protocol.CoordPeer{}, err
			}
			touched = time.Now()
		}
		var status protocol.CoordStatusResult
		if err := call(ctx, cs, toolStatus, nil, &status); err != nil {
			return status, protocol.CoordPeer{}, err
		}
		if len(status.Peers) > 0 {
			return status, status.Peers[0], nil
		}
		if err := wait(ctx, deadline); err != nil {
			return status, protocol.CoordPeer{}, fmt.Errorf("no authorized peer: %w", err)
		}
	}
}

// waitInbox polls aether_inbox until a peer's message is delivered.
func waitInbox(ctx context.Context, cs *mcp.ClientSession) (protocol.CoordMessage, error) {
	deadline := time.Now().Add(poll)
	for {
		var inbox struct {
			Messages []protocol.CoordMessage `json:"messages"`
		}
		if err := call(ctx, cs, toolInbox, nil, &inbox); err != nil {
			return protocol.CoordMessage{}, err
		}
		if len(inbox.Messages) > 0 {
			return inbox.Messages[0], nil
		}
		if err := wait(ctx, deadline); err != nil {
			return protocol.CoordMessage{}, fmt.Errorf("empty inbox: %w", err)
		}
	}
}

func wait(ctx context.Context, deadline time.Time) error {
	if time.Now().After(deadline) {
		return fmt.Errorf("gave up after %s", poll)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

// call makes one MCP tool call and decodes its structured result.
func call(ctx context.Context, cs *mcp.ClientSession, name string, args, out any) error {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		return fmt.Errorf("%s reported a tool error: %+v", name, res.Content)
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return fmt.Errorf("%s: re-marshal result: %w", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode result: %w", name, err)
	}
	return nil
}

func drainStdin() {
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		say("notice:%s", scan.Text())
	}
}

// say writes one line to the terminal the test reads over its attach. The
// container runs on a TTY, so lines end CRLF.
func say(format string, args ...any) {
	fmt.Printf(format+"\r\n", args...)
}
