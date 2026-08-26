//go:build integration

package sshd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// writeClientKey writes an OpenSSH-format ed25519 private key for the ssh
// binary and returns its path plus the matching signer.
func writeClientKey(t *testing.T, dir, name string) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return path, signer
}

func requireBinary(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("integration tests need a real %s binary: %v", name, err)
	}
	return path
}

func sshArgs(keyPath, port string) []string {
	return []string{
		"-T",
		"-p", port,
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"aether@127.0.0.1",
	}
}

// newIntegrationEnv builds a testEnv whose member key also exists on disk
// for the real ssh client.
func newIntegrationEnv(t *testing.T, mod func(*Config)) (*testEnv, string) {
	t.Helper()
	keyDir := t.TempDir()
	keyPath, signer := writeClientKey(t, keyDir, "id_ed25519")
	e := newTestEnvWithSigner(t, mod, signer)
	return e, keyPath
}

func TestIntegrationControlChannelOverRealSSH(t *testing.T) {
	sshBin := requireBinary(t, "ssh")
	e, keyPath := newIntegrationEnv(t, nil)

	cmd := exec.Command(sshBin, append(sshArgs(keyPath, e.port()), "-s", "aether-control")...)
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server.info","params":{}}` + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ssh -s aether-control: %v (output %q)", err, out)
	}
	var resp protocol.Response
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out, err)
	}
	if resp.Error != nil {
		t.Fatalf("server.info error: %+v", resp.Error)
	}
	var info protocol.ServerInfoResult
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if info.ProtocolVersion != protocol.Version || info.Member.ID != string(e.member.ID) {
		t.Errorf("server.info = %+v", info)
	}
}

func TestIntegrationUnknownKeyRejectedOverRealSSH(t *testing.T) {
	sshBin := requireBinary(t, "ssh")
	e, _ := newIntegrationEnv(t, nil)
	strangerKey, _ := writeClientKey(t, t.TempDir(), "id_stranger")

	cmd := exec.Command(sshBin, append(sshArgs(strangerKey, e.port()), "-s", "aether-control")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("")
	if err := cmd.Run(); err == nil {
		t.Fatal("ssh with an unregistered key succeeded")
	}
	if !strings.Contains(stderr.String(), "no Aether member for this key") {
		t.Errorf("stderr = %q, want the rejection banner", stderr.String())
	}
}

func TestIntegrationEventStreamOverRealSSH(t *testing.T) {
	sshBin := requireBinary(t, "ssh")
	e, keyPath := newIntegrationEnv(t, nil)

	cmd := exec.Command(sshBin, append(sshArgs(keyPath, e.port()), "-s", "aether-events")...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ssh: %v", err)
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	if _, err := io.WriteString(stdin, `{"run_id":"`+string(e.run.ID)+`"}`+"\n"); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	r := bufio.NewReader(stdout)
	var ack protocol.SubscribeResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		t.Fatalf("subscribe ack = %+v", ack)
	}

	if _, err := e.bus.Publish(context.Background(), events.Event{
		WorkspaceID: e.ws.ID, RunID: e.run.ID,
		Payload: events.RunStatusPayload{To: "running"},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	var ev protocol.Event
	readJSONLine(t, r, &ev)
	if ev.Type != "run.status" || ev.RunID != string(e.run.ID) {
		t.Errorf("event = %+v", ev)
	}
}

func TestIntegrationGitLsRemoteReachesTransport(t *testing.T) {
	gitBin := requireBinary(t, "git")
	requireBinary(t, "ssh")
	e, keyPath := newIntegrationEnv(t, nil)

	cmd := exec.Command(gitBin, "ls-remote", fmt.Sprintf("ssh://aether@127.0.0.1:%s/%s.git", e.port(), e.ws.ID))
	cmd.Env = append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+keyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-remote: %v (stderr %q)", err, stderr.String())
	}
	if !strings.Contains(string(out), testAdvertisedSHA) || !strings.Contains(string(out), "refs/heads/main") {
		t.Errorf("ls-remote output = %q, want the advertised ref", out)
	}
	if calls := e.git.Calls(); len(calls) != 1 || calls[0] != "upload-pack:"+string(e.ws.ID) {
		t.Errorf("git transport calls = %v, want one upload-pack", calls)
	}
}

func (e *testEnv) port() string {
	_, port, _ := net.SplitHostPort(e.addr)
	return port
}
