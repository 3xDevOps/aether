//go:build integration

package scheduler

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	claudeCodeVersion = "2.1.317"
	claudeCodeSHA256  = "c33bff413c33274e404cd57378300c6a389558a4e757f9af64539326d1c53fb2"
	claudeCodeURL     = "https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-" + claudeCodeVersion + ".tgz"
	claudeCodeImage   = "debian:12-slim"
)

// claudeCodeBinary is the pinned Claude Code native binary: the file named by
// AETHER_TEST_CLAUDE_CODE, else the npm package downloaded once into the user
// cache and checked against its digest.
func claudeCodeBinary(t *testing.T) string {
	t.Helper()
	if name := os.Getenv("AETHER_TEST_CLAUDE_CODE"); name != "" {
		return name
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("the pinned Claude Code binary is linux/amd64, not %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no cache directory for the Claude Code binary: %v", err)
	}
	dir := filepath.Join(cache, "aether-test")
	name := filepath.Join(dir, "claude-"+claudeCodeVersion)
	if _, err = os.Stat(name); err == nil {
		return name
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(claudeCodeURL)
	if err != nil {
		t.Skipf("download Claude Code %s: %v", claudeCodeVersion, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("download Claude Code %s: %s", claudeCodeVersion, resp.Status)
	}
	sum := sha256.New()
	zr, err := gzip.NewReader(io.TeeReader(resp.Body, sum))
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(dir, "claude-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("package/claude not in the Claude Code package: %v", err)
		}
		if h.Name == "package/claude" {
			if _, err = io.Copy(tmp, tr); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if _, err = io.Copy(io.Discard, zr); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != claudeCodeSHA256 {
		t.Fatalf("Claude Code %s package sha256 = %s, want %s", claudeCodeVersion, got, claudeCodeSHA256)
	}
	if err = tmp.Chmod(0o755); err != nil {
		t.Fatal(err)
	}
	if err = tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(tmp.Name(), name); err != nil {
		t.Fatal(err)
	}
	return name
}

// anthropicMock is the Anthropic API and sign-in service the CLI and the
// server talk to: it records each request's Authorization header.
type anthropicMock struct {
	mu   sync.Mutex
	auth []string
}

func (m *anthropicMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.auth = append(m.auth, r.URL.Path+" "+r.Header.Get("Authorization"))
	m.mu.Unlock()
	switch r.URL.Path {
	case "/v1/messages":
		if !strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range [][2]string{
			{"message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e[0], e[1])
		}
	case "/claude/token":
		fmt.Fprint(w, `{"access_token":"fake-grace-access-2","refresh_token":"fake-grace-refresh-2","expires_in":3600,"scope":"user:inference user:profile"}`)
	case "/claude/profile":
		fmt.Fprint(w, `{"account":{"uuid":"grace"},"organization":{"uuid":"org-grace"}}`)
	default:
		http.NotFound(w, r)
	}
}

// since returns the requests recorded after the first n.
func (m *anthropicMock) since(n int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.auth[n:]...)
}

func (m *anthropicMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.auth)
}

// messages is the Authorization headers of the /v1/messages requests in reqs.
func messages(reqs []string) []string {
	var out []string
	for _, r := range reqs {
		if auth, ok := strings.CutPrefix(r, "/v1/messages "); ok {
			out = append(out, auth)
		}
	}
	return out
}

// TestIntegrationSessionLoginRealClaudeCode runs the real Claude Code CLI in
// Ada's session on Grace's listed account, on an internal Docker network
// whose only reachable host is a mock of the Anthropic API. The CLI signs
// in as Grace, never as Ada; follows Grace's refreshed login in the same
// container without ever holding a refresh token; and is signed out within
// one sync pass of the revoke.
func TestIntegrationSessionLoginRealClaudeCode(t *testing.T) {
	bin := claudeCodeBinary(t)
	ctx := t.Context()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if _, err = cli.ImageInspect(ctx, claudeCodeImage); err != nil {
		if out, err := exec.Command("docker", "pull", claudeCodeImage).CombinedOutput(); err != nil {
			t.Fatalf("docker pull %s: %v: %s", claudeCodeImage, err, out)
		}
	}
	network := "aether-test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if out, err := exec.Command("docker", "network", "create", "--internal", network).CombinedOutput(); err != nil {
		t.Fatalf("docker network create: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })
	gateway, err := exec.Command("docker", "network", "inspect", network, "-f", "{{(index .IPAM.Config 0).Gateway}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimSpace(string(gateway))
	lis, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Fatalf("listen on the %s network's gateway: %v", network, err)
	}
	mock := &anthropicMock{}
	srv := httptest.NewUnstartedServer(mock)
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)

	// The CLI's own request path: HOME for the exec'd shell, the mock for
	// the API, and no traffic beyond inference.
	ask := "cd /tmp && HOME=/root ANTHROPIC_BASE_URL=" + srv.URL + " CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 /root/.local/bin/claude -p ping 2>&1"
	e, docker, _, owner := newShareDockerEnvOn(t, []string{"sh", "-c", `sleep 1; printf 'harness-login:%s\n' "$(` + ask + `)"; sleep 1`}, network, claudeCodeImage)
	if _, err = e.db.ShareAccount(ctx, owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.cfg.SessionLoginAccounts = map[domain.MemberID]bool{owner.ID: true}
	e.sched.cfg.AssistantIssuer = AssistantIssuer{ClaudeToken: srv.URL + "/claude/token", ClaudeProfile: srv.URL + "/claude/profile", Client: srv.Client()}
	ownerHome, err := e.cfg.Homes.Path(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	adaHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(8 * time.Hour)
	seedHome(t, ownerHome, map[string]string{claudeLogin: claudeOwnerLogin("fake-grace-access-1", "fake-grace-refresh-1", later)})
	seedHome(t, adaHome, map[string]string{claudeLogin: claudeOwnerLogin("fake-ada-access-1", "fake-ada-refresh-1", later)})
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	seedHome(t, adaHome, map[string]string{".local/bin/claude": string(data)})
	if err = os.Chmod(filepath.Join(adaHome, ".local/bin/claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	ownerLogin := filepath.Join(ownerHome, claudeLogin)
	inode := inodeOf(t, ownerLogin)

	shared := launchShareRun(t, e, docker, cli, e.member.ID, owner.ID, "real claude", "pong")
	if got := messages(mock.since(0)); len(got) != 1 || got[0] != "Bearer fake-grace-access-1" {
		t.Fatalf("launch: /v1/messages Authorization headers = %q, want Grace's first token once", got)
	}
	if code, out := shared.sh(t, "/root/.local/bin/claude --version"); code != 0 || !strings.HasPrefix(out, claudeCodeVersion+" ") {
		t.Fatalf("claude --version = exit %d, %q; want %s", code, out, claudeCodeVersion)
	}
	if code, out := shared.sh(t, "timeout 3 bash -c 'echo > /dev/tcp/1.1.1.1/443'"); code == 0 {
		t.Fatalf("the session reached 1.1.1.1 from the internal network: %s", out)
	}

	// Grace's login falls due: the pump refreshes it in place and the same
	// container's next request carries the rotated token.
	if err = os.WriteFile(ownerLogin, []byte(claudeOwnerLogin("fake-grace-access-1", "fake-grace-refresh-1", time.Now().Add(5*time.Minute))), 0o600); err != nil {
		t.Fatal(err)
	}
	n := mock.count()
	e.sched.syncSessionLogins(ctx)
	if got := mock.since(n); len(got) != 2 || got[0] != "/claude/token " || got[1] != "/claude/profile Bearer fake-grace-access-2" {
		t.Fatalf("central refresh requests = %q, want the token exchange then the profile check", got)
	}
	if got := inodeOf(t, ownerLogin); got != inode {
		t.Fatalf("Grace's login inode = %d, want %d", got, inode)
	}
	if got := readFile(t, ownerLogin); !strings.Contains(got, "fake-grace-access-2") || !strings.Contains(got, "fake-grace-refresh-2") {
		t.Fatalf("Grace's login after the refresh = %s", got)
	}
	copyFile := e.sched.sessionLoginPath(shared.run.ID, owner.ID, "claude")
	if got := readFile(t, copyFile); strings.Contains(got, "refresh") || strings.Contains(got, "mcp-secret") || !strings.Contains(got, "fake-grace-access-2") {
		t.Fatalf("session login copy = %s, want the rotated access token and no refresh token", got)
	}
	n = mock.count()
	code, out := shared.sh(t, ask)
	if code != 0 || !strings.Contains(out, "pong") {
		t.Fatalf("claude after the refresh = exit %d, %q", code, out)
	}
	if got := messages(mock.since(n)); len(got) != 1 || got[0] != "Bearer fake-grace-access-2" {
		t.Fatalf("after the refresh: /v1/messages Authorization headers = %q, want the rotated token", got)
	}

	// The revoke: one pump pass signs the copy out and the CLI sends nothing.
	if revoked, err := e.db.RevokeAccountShare(ctx, owner.ID, e.member.ID); err != nil || !revoked {
		t.Fatalf("RevokeAccountShare = %v, %v", revoked, err)
	}
	e.sched.syncSessionLogins(ctx)
	if got := readFile(t, copyFile); got != "{}" {
		t.Fatalf("session login copy after the revoke = %s, want {}", got)
	}
	n = mock.count()
	code, out = shared.sh(t, ask)
	t.Logf("claude after the revoke: exit %d, %q", code, out)
	if code == 0 || !strings.Contains(out, "Not logged in") {
		t.Fatalf("claude after the revoke = exit %d, %q; want it signed out", code, out)
	}
	for _, r := range mock.since(n) {
		if strings.Contains(r, "Bearer") {
			t.Fatalf("a request after the revoke carried a token: %q", mock.since(n))
		}
	}
	for _, r := range mock.since(0) {
		if strings.Contains(r, "ada") {
			t.Fatalf("a request carried Ada's token: %q", r)
		}
	}
	t.Logf("request log: %q", mock.since(0))
}
