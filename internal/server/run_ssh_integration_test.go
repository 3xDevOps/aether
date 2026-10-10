//go:build integration

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// sshUser is one member's laptop: a linked aether CLI, a home with the ssh
// config `aether ssh-config` wrote into it, and the system ssh clients run
// against that config.
type sshUser struct {
	t      *testing.T
	aether string
	env    []string
	config string
}

func newSSHUser(t *testing.T, aether, addr, keyPath string) *sshUser {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(configDir, "aether"), 0o700); err != nil {
		t.Fatal(err)
	}
	link, err := json.Marshal(cli.Config{Addr: addr, Key: keyPath, KnownHosts: filepath.Join(home, "known_hosts")})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "aether", "config.json"), string(link))
	// A Host * of the member's own, which must not rename the run or send
	// it somewhere else.
	if err = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".ssh", "config"), "Host *\n  HostName elsewhere.invalid\n  ProxyJump bastion.invalid\n")
	u := &sshUser{
		t: t, aether: aether, config: filepath.Join(home, ".ssh", "config"),
		// No agent: the member's key is the one the link names.
		env: append(os.Environ(), "HOME="+home, cli.ConfigDirEnv+"="+configDir, "SSH_AUTH_SOCK="),
	}
	if stdout, stderr, code := u.run("", aether, "ssh-config"); code != 0 {
		t.Fatalf("aether ssh-config exited %d: %s%s", code, stdout, stderr)
	}
	return u
}

// command is the system client with this member's configuration. ssh reads
// ~/.ssh/config from the account's home, not $HOME, so the file is named.
func (u *sshUser) command(name string, args ...string) *exec.Cmd {
	if name != u.aether {
		args = append([]string{"-F", u.config}, args...)
	}
	cmd := exec.Command(name, args...)
	cmd.Env = u.env
	return cmd
}

func (u *sshUser) run(stdin, name string, args ...string) (stdout, stderr string, code int) {
	u.t.Helper()
	cmd := u.command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		u.t.Fatalf("%s %v: %v", name, args, err)
	}
	return out.String(), errOut.String(), code
}

func buildCLIBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aether")
	build := exec.Command("go", "build", "-o", path, "./cmd/aether")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the aether CLI: %v (%s)", err, out)
	}
	return path
}

// httpGet polls a forwarded port until the server behind it answers.
func httpGet(t *testing.T, port int) string {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			body, rerr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if rerr == nil && resp.StatusCode == http.StatusOK {
				return string(body)
			}
			err = fmt.Errorf("status %d: %v", resp.StatusCode, rerr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET through the forward on port %d: %v", port, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestIntegrationRunSSH drives the system ssh, scp and sftp clients into a
// real run container the way a member's laptop does after `aether
// ssh-config`: commands with their exit status, a terminal, files in both
// directions, a forward to a listener bound only to the container's
// loopback, the bootstrap an editor's remote server performs, and every
// refusal with the server's reason.
func TestIntegrationRunSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("ssh into a run needs a real container; Docker daemon unreachable")
	}
	for _, name := range []string{"ssh", "scp", "sftp"} {
		requireBinary(t, name)
	}
	image, user := buildCoordAgentImage(t)
	docker, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("Docker disappeared")
	}
	e := &coordEnv{rt: docker, image: image, serverBinary: buildServerBinary(t), dataDir: filepath.Join(shortTempDir(t), "data")}
	server := e.seed(ctx, t, true)
	ctrl, _ := server.control(t, e.ada.key)
	run := e.launch(t, ctrl, "ssh into the run", "claude")
	id := domain.RunID(run.ID)
	closed := false
	defer func() {
		if closed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := server.srv.sched.CloseRun(cleanupCtx, id, e.ada.id, domain.RunAbandoned); err != nil {
			t.Errorf("clean the run: %v", err)
		}
	}()

	aether := buildCLIBinary(t)
	ada := newSSHUser(t, aether, server.addr, e.keyPath)
	host := run.ID + cli.RunHostSuffix
	// The CLI records the server's host key on first contact and says so.
	if _, stderr, code := ada.run("", "ssh", host, "true"); code != 0 {
		t.Fatalf("first ssh into the run exited %d: %s", code, stderr)
	}

	// A command runs in the checkout as the run's user with the run's
	// environment, with nothing added to either stream.
	stdout, stderr, code := ada.run("", "ssh", host, `echo "$PWD"; id -u; echo "$AETHER_RUN_ID"; echo oops >&2; exit 7`)
	want := "/workspace\n" + strings.Split(user, ":")[0] + "\n" + run.ID + "\n"
	if stdout != want || stderr != "oops\n" || code != 7 {
		t.Fatalf("exec = stdout %q stderr %q status %d, want %q, \"oops\\n\" and 7", stdout, stderr, code, want)
	}
	if stdout, stderr, code = ada.run("", aether, "ssh", run.ID, "echo via-aether; exit 5"); stdout != "via-aether\n" || code != 5 {
		t.Fatalf("aether ssh = stdout %q stderr %q status %d", stdout, stderr, code)
	}

	// An interactive login shell on a real terminal.
	stdout, stderr, code = ada.run("tty; stty size; exit 4\n", "ssh", "-tt", host)
	if !strings.Contains(stdout, "/dev/pts/") || !strings.Contains(stdout, "24 80") || code != 4 {
		t.Fatalf("terminal session = stdout %q stderr %q status %d, want a pts device, 24 80 and status 4", stdout, stderr, code)
	}

	// Files in both directions, with relative paths in the checkout.
	local := t.TempDir()
	writeFile(t, filepath.Join(local, "up.txt"), "from the laptop\n")
	if _, stderr, code = ada.run("", "scp", filepath.Join(local, "up.txt"), host+":scp-up.txt"); code != 0 {
		t.Fatalf("scp to the run exited %d: %s", code, stderr)
	}
	if _, stderr, code = ada.run("", "scp", host+":README.md", filepath.Join(local, "scp-down.md")); code != 0 {
		t.Fatalf("scp from the run exited %d: %s", code, stderr)
	}
	batch := fmt.Sprintf("put %s sftp-up.txt\nget scp-up.txt %s\n", filepath.Join(local, "up.txt"), filepath.Join(local, "sftp-down.txt"))
	if _, stderr, code = ada.run(batch, "sftp", "-b", "-", host); code != 0 {
		t.Fatalf("sftp batch exited %d: %s", code, stderr)
	}
	if stdout, _, _ = ada.run("", "ssh", host, "cat /workspace/scp-up.txt /workspace/sftp-up.txt"); stdout != "from the laptop\nfrom the laptop\n" {
		t.Fatalf("uploaded files in the checkout = %q", stdout)
	}
	for name, content := range map[string]string{"scp-down.md": "# coordination seed\n", "sftp-down.txt": "from the laptop\n"} {
		got, err := os.ReadFile(filepath.Join(local, name))
		if err != nil || string(got) != content {
			t.Fatalf("downloaded %s = %q %v, want %q", name, got, err, content)
		}
	}

	// A forward reaches a listener bound only to the container's loopback,
	// which the container's own address does not serve.
	const appPort = 31873
	app := ada.command("ssh", host, fmt.Sprintf(`mkdir -p /tmp/app && echo loopback-only > /tmp/app/index.html && exec httpd -f -p 127.0.0.1:%d -h /tmp/app`, appPort))
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Process.Kill(); _ = app.Wait() }()
	forwardPort := freePort(t)
	forward := ada.command("ssh", "-N", "-L", fmt.Sprintf("%d:localhost:%d", forwardPort, appPort), host)
	if err := forward.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = forward.Process.Kill(); _ = forward.Wait() }()
	if body := httpGet(t, forwardPort); body != "loopback-only\n" {
		t.Fatalf("forwarded response = %q", body)
	}
	addr, err := server.srv.sched.ContainerAddr(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if conn, derr := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", addr, appPort), 3*time.Second); derr == nil {
		_ = conn.Close()
		t.Fatal("the loopback-only listener answers on the container's address")
	}
	if _, stderr, code = ada.run("", "ssh", "-W", "example.com:80", host); code == 0 || !strings.Contains(stderr, "only the run's own loopback") {
		t.Fatalf("forward to another host = status %d stderr %q, want it prohibited", code, stderr)
	}

	// What an editor does when it attaches: pipe a script to a shell,
	// read what the script prints, then reach the server the script left
	// running through a forward on the same connection.
	const editorPort = 31874
	editorForward := freePort(t)
	editor := ada.command("ssh", "-T", "-L", fmt.Sprintf("%d:127.0.0.1:%d", editorForward, editorPort), host, "sh")
	script, err := editor.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := editor.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var editorErr bytes.Buffer
	editor.Stderr = &editorErr
	if err = editor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = editor.Process.Kill() }()
	if _, err = fmt.Fprintf(script, `set -e
mkdir -p "$HOME/.editor-server"
echo editor-server-up > "$HOME/.editor-server/index.html"
echo 'f00dfeed: start'
httpd -p 127.0.0.1:%d -h "$HOME/.editor-server"
echo 'listeningOn==%d=='
echo 'f00dfeed: end'
`, editorPort, editorPort); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(output)
	var printed []string
	for lines.Scan() && lines.Text() != "f00dfeed: end" {
		printed = append(printed, lines.Text())
	}
	if got := strings.Join(printed, "\n"); got != fmt.Sprintf("f00dfeed: start\nlisteningOn==%d==", editorPort) {
		t.Fatalf("bootstrap output = %q (stderr %q)", got, editorErr.String())
	}
	if body := httpGet(t, editorForward); body != "editor-server-up\n" {
		t.Fatalf("editor server response = %q", body)
	}
	if err = script.Close(); err != nil {
		t.Fatal(err)
	}
	if err = editor.Wait(); err != nil {
		t.Fatalf("bootstrap session: %v (stderr %q)", err, editorErr.String())
	}
	// A session's processes end with it, the detached server included.
	probe := fmt.Sprintf("wget -q -O - http://127.0.0.1:%d/ 2>/dev/null || echo stopped", editorPort)
	if stdout, _, _ = ada.run("", "ssh", host, probe); stdout != "stopped\n" {
		t.Fatalf("the editor server outlived its session: %q", stdout)
	}

	// The connections above are on the timeline under Ada's name.
	var history protocol.WorkspaceTimelineResult
	if err = ctrl.Call(protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{
		WorkspaceID: string(e.ws.ID), RunID: run.ID, Types: []string{string(events.TypeTimeline)},
	}, &history); err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, ev := range history.Events {
		var entry events.TimelinePayload
		if err = json.Unmarshal(ev.Payload, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Message == "connected over SSH" {
			if ev.ActorID != string(e.ada.id) {
				t.Fatalf("ssh connection recorded for %s, want %s", ev.ActorID, e.ada.id)
			}
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("no timeline entry for the ssh connection in %d events", len(history.Events))
	}

	// A collaborator Ada has not shared her account with is refused with
	// the reason a shell tab gives, and nothing runs.
	cyKey, cySigner := writeClientKey(t)
	cy := &domain.Member{
		DisplayName: "Cy", PublicKey: string(ssh.MarshalAuthorizedKey(cySigner.PublicKey())),
		Color: "#4363d8", Role: domain.RoleCollaborator,
	}
	if err = server.srv.Store().CreateMember(ctx, cy); err != nil {
		t.Fatal(err)
	}
	stranger := newSSHUser(t, aether, server.addr, cyKey)
	if stdout, stderr, code = stranger.run("", "ssh", host, "echo reached"); code != 255 || stdout != "" ||
		!strings.Contains(stderr, "has not shared their account with you") {
		t.Fatalf("unshared collaborator = stdout %q stderr %q status %d", stdout, stderr, code)
	}

	// A paused run is refused by name. The forward opened before the pause
	// is frozen with the container, not dropped, and carries on after the
	// resume; the pause is held past the server's revalidation interval so
	// that a dropped connection would show.
	if err = ctrl.Call(protocol.MethodRunPause, protocol.RunIDParams{RunID: run.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code = ada.run("", "ssh", host, "true"); code != 255 || !strings.Contains(stderr, "the run is paused") {
		t.Fatalf("paused run = status %d stderr %q", code, stderr)
	}
	time.Sleep(4 * time.Second)
	if err = ctrl.Call(protocol.MethodRunResume, protocol.RunIDParams{RunID: run.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, code = ada.run("", "ssh", host, "echo resumed"); stdout != "resumed\n" || code != 0 {
		t.Fatalf("resumed run = stdout %q stderr %q status %d", stdout, stderr, code)
	}
	if body := httpGet(t, forwardPort); body != "loopback-only\n" {
		t.Fatalf("forward after the resume = %q", body)
	}

	// Dropping a connection stops what its session was running.
	_ = app.Process.Kill()
	_ = app.Wait()
	appProbe := fmt.Sprintf("wget -q -O - http://127.0.0.1:%d/ 2>/dev/null || echo stopped", appPort)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		if stdout, _, _ = ada.run("", "ssh", host, appProbe); stdout == "stopped\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the listener outlived its dropped session: %q", stdout)
		}
	}

	closed = true
	if err = server.srv.sched.CloseRun(ctx, id, e.ada.id, domain.RunAbandoned); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code = ada.run("", "ssh", host, "true"); code != 255 || !strings.Contains(stderr, "the run is abandoned") {
		t.Fatalf("closed run = status %d stderr %q", code, stderr)
	}
}
