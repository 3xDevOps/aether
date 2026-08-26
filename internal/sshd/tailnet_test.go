package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeWhoIs is a switchable WhoIsResolver: tests point it at the identity
// (or error) the next connections should resolve to.
type fakeWhoIs struct {
	mu  sync.Mutex
	id  WhoIsIdentity
	err error
}

func (f *fakeWhoIs) set(id WhoIsIdentity, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.id, f.err = id, err
}

func (f *fakeWhoIs) WhoIs(context.Context, string) (WhoIsIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id, f.err
}

// dialAs dials with an explicit SSH username and auth methods (none when
// empty - the pure tailnet client shape).
func (e *testEnv) dialAs(user string, banner *strings.Builder, auth ...ssh.AuthMethod) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	if banner != nil {
		cfg.BannerCallback = func(message string) error {
			banner.WriteString(message)
			return nil
		}
	}
	return ssh.Dial("tcp", e.addr, cfg)
}

func controlClientOn(t *testing.T, client *ssh.Client) *protocol.Client {
	t.Helper()
	pipe := openSubsystem(t, client, protocol.SubsystemControl, nil)
	return protocol.NewClient(pipe)
}

func serverInfoMember(t *testing.T, c *protocol.Client) protocol.Member {
	t.Helper()
	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("server.info: %v", err)
	}
	return info.Member
}

// lockedBuffer collects concurrent slog output from server goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// Acceptance 1: a fresh connection from a tailnet client with no SSH key
// bootstraps the admin.
func TestTailnetBootstrapAdmin(t *testing.T) {
	logs := captureLogs(t)
	whois := &fakeWhoIs{id: WhoIsIdentity{Login: "alice@example.com", NodeID: "node-1"}}
	e := newFreshTestEnv(t, func(c *Config) { c.WhoIs = whois })

	client, err := e.dialAs("alice", nil)
	if err != nil {
		t.Fatalf("tailnet dial without key: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	m := serverInfoMember(t, controlClientOn(t, client))
	if m.Role != string(domain.RoleAdmin) {
		t.Errorf("bootstrap role = %q, want admin", m.Role)
	}
	if m.Pending {
		t.Error("bootstrap member must not be pending")
	}
	stored, err := e.store.GetMemberByTailnetLogin(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("member by tailnet login: %v", err)
	}
	if stored.PublicKey != "" {
		t.Errorf("tailnet bootstrap member has a key: %q", stored.PublicKey)
	}
	if got := logs.String(); !strings.Contains(got, "bootstrap") ||
		!strings.Contains(got, "alice@example.com") || !strings.Contains(got, "node-1") {
		t.Errorf("bootstrap audit log missing identity: %q", got)
	}
}

// Acceptance 2: a second tailnet identity lands pending, is denied
// everything but server.info, and member.approve unblocks it.
func TestTailnetSecondIdentityPendingThenApproved(t *testing.T) {
	whois := &fakeWhoIs{id: WhoIsIdentity{Login: "alice@example.com", NodeID: "node-1"}}
	e := newFreshTestEnv(t, func(c *Config) { c.WhoIs = whois })

	adminClient, err := e.dialAs("alice", nil)
	if err != nil {
		t.Fatalf("bootstrap dial: %v", err)
	}
	t.Cleanup(func() { _ = adminClient.Close() })
	admin := controlClientOn(t, adminClient)
	adminID := serverInfoMember(t, admin).ID

	whois.set(WhoIsIdentity{Login: "bob@example.com", NodeID: "node-2"}, nil)
	bobClient, err := e.dialAs("bob", nil)
	if err != nil {
		t.Fatalf("second identity dial: %v", err)
	}
	t.Cleanup(func() { _ = bobClient.Close() })
	bob := controlClientOn(t, bobClient)

	bobInfo := serverInfoMember(t, bob)
	if !bobInfo.Pending {
		t.Fatal("second identity must land pending")
	}
	if bobInfo.ID == adminID {
		t.Fatal("second identity mapped to the bootstrap member")
	}

	var pe *protocol.Error
	if listErr := bob.Call(protocol.MethodMemberList, struct{}{}, nil); !errors.As(listErr, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending member.list = %v, want CodeDenied", listErr)
	}
	if !strings.Contains(pe.Message, "pending") {
		t.Errorf("denial message %q does not explain the pending state", pe.Message)
	}

	// Approval is admin-only: bob cannot approve himself.
	err = bob.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: bobInfo.ID}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("pending self-approve = %v, want CodeDenied", err)
	}

	var res protocol.MemberApproveResult
	if approveErr := admin.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: bobInfo.ID}, &res); approveErr != nil {
		t.Fatalf("member.approve: %v", approveErr)
	}
	if res.Member.Pending {
		t.Error("approved member still pending in result")
	}

	// The already-established pending connection is unblocked: list and
	// steer now work per role.
	var ml protocol.MemberListResult
	if listErr := bob.Call(protocol.MethodMemberList, struct{}{}, &ml); listErr != nil {
		t.Fatalf("approved member.list: %v", listErr)
	}
	if len(ml.Members) != 2 {
		t.Errorf("member.list len = %d, want 2", len(ml.Members))
	}
	// Steering resolves the target run for the permission check, so give
	// bob a real run to inject into.
	ws := &domain.Workspace{Name: "proj", BaseBranch: "main", Environment: domain.WorkspaceEnvironment{CustomImage: "img"}}
	if cerr := e.store.CreateWorkspace(context.Background(), ws); cerr != nil {
		t.Fatalf("create workspace: %v", cerr)
	}
	run := &domain.Run{
		WorkspaceID: ws.ID, MemberID: domain.MemberID(bobInfo.ID), Task: "t",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if cerr := e.store.CreateRun(context.Background(), run); cerr != nil {
		t.Fatalf("create run: %v", cerr)
	}
	if injectErr := bob.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: string(run.ID), Message: "go"}, nil); injectErr != nil {
		t.Fatalf("approved run.inject: %v", injectErr)
	}

	// Approval is admin-only even for approved non-admins.
	err = bob.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: bobInfo.ID}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("non-admin member.approve = %v, want CodeDenied", err)
	}
}

// Acceptance 3: with tailscaled down (resolver errors) key members still
// connect; tailnet-only members are refused with a clear banner.
func TestTailnetResolverErrorFallsBackToKeys(t *testing.T) {
	whois := &fakeWhoIs{err: errors.New("tailscaled unreachable")}
	e := newTestEnv(t, func(c *Config) { c.WhoIs = whois })

	client, err := e.dialAs("ada", nil, ssh.PublicKeys(e.signer))
	if err != nil {
		t.Fatalf("key fallback dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if got := serverInfoMember(t, controlClientOn(t, client)).ID; got != string(e.member.ID) {
		t.Errorf("fallback identity = %q, want the key member %q", got, e.member.ID)
	}

	tailnetOnly := &domain.Member{
		DisplayName: "Bob", TailnetLogin: "bob@example.com",
		Color: "#3cb44b", Role: domain.RoleCollaborator,
	}
	if err := e.store.CreateMember(context.Background(), tailnetOnly); err != nil {
		t.Fatalf("create tailnet-only member: %v", err)
	}
	var banner strings.Builder
	if c, err := e.dialAs("bob", &banner); err == nil {
		_ = c.Close()
		t.Fatal("tailnet-only member connected while the resolver is down")
	}
	if !strings.Contains(banner.String(), "tailnet identity unavailable") {
		t.Errorf("banner = %q, want the whois-failure explanation", banner.String())
	}
}

// Acceptance 4: a tagged node is never mapped through WhoIs - a
// registered key authenticates it, no key produces the clear banner.
func TestTailnetTaggedNodeRequiresKey(t *testing.T) {
	whois := &fakeWhoIs{id: WhoIsIdentity{NodeID: "node-ci", Tagged: true}}
	e := newTestEnv(t, func(c *Config) { c.WhoIs = whois })

	client, err := e.dialAs("ci", nil, ssh.PublicKeys(e.signer))
	if err != nil {
		t.Fatalf("tagged node with registered key: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if got := serverInfoMember(t, controlClientOn(t, client)).ID; got != string(e.member.ID) {
		t.Errorf("tagged-node identity = %q, want the key member %q", got, e.member.ID)
	}

	var banner strings.Builder
	if c, err := e.dialAs("ci", &banner); err == nil {
		_ = c.Close()
		t.Fatal("tagged node connected without a key")
	}
	if !strings.Contains(banner.String(), "tagged tailnet node") {
		t.Errorf("banner = %q, want the tagged-node explanation", banner.String())
	}

	banner.Reset()
	if c, err := e.dialAs("ci", &banner, ssh.PublicKeys(newSigner(t))); err == nil {
		_ = c.Close()
		t.Fatal("tagged node connected with an unregistered key")
	}
	if !strings.Contains(banner.String(), "no Aether member for this key") {
		t.Errorf("banner = %q, want the unknown-key rejection", banner.String())
	}
}

// Acceptance 5a: two OS users on one untagged node resolve to the same
// member, and the audit line records node ID and tailnet login.
func TestTailnetSameNodeTwoOSUsers(t *testing.T) {
	logs := captureLogs(t)
	whois := &fakeWhoIs{id: WhoIsIdentity{Login: "alice@example.com", NodeID: "node-1"}}
	e := newFreshTestEnv(t, func(c *Config) { c.WhoIs = whois })

	var ids [2]string
	for i, user := range []string{"root", "deploy"} {
		client, err := e.dialAs(user, nil)
		if err != nil {
			t.Fatalf("dial as %s: %v", user, err)
		}
		t.Cleanup(func() { _ = client.Close() })
		ids[i] = serverInfoMember(t, controlClientOn(t, client)).ID
	}
	if ids[0] != ids[1] {
		t.Errorf("two OS users mapped to different members: %q vs %q", ids[0], ids[1])
	}
	if got := logs.String(); !strings.Contains(got, "node-1") || !strings.Contains(got, "alice@example.com") {
		t.Errorf("audit log missing node ID or login: %q", got)
	}
}

// Acceptance 5b: with tailnet_require_key on, a tailnet connection
// without a registered key is refused; with the key it authenticates.
func TestTailnetRequireKey(t *testing.T) {
	whois := &fakeWhoIs{id: WhoIsIdentity{Login: "ada@example.com", NodeID: "node-1"}}
	e := newTestEnv(t, func(c *Config) {
		c.WhoIs = whois
		c.TailnetRequireKey = true
	})

	var banner strings.Builder
	if c, err := e.dialAs("ada", &banner); err == nil {
		_ = c.Close()
		t.Fatal("require_key on: keyless tailnet connection succeeded")
	}
	if !strings.Contains(banner.String(), "must also present a registered SSH key") {
		t.Errorf("banner = %q, want the require-key explanation", banner.String())
	}

	client, err := e.dialAs("ada", nil, ssh.PublicKeys(e.signer))
	if err != nil {
		t.Fatalf("require_key on, key dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if got := serverInfoMember(t, controlClientOn(t, client)).ID; got != string(e.member.ID) {
		t.Errorf("require_key identity = %q, want the key member %q", got, e.member.ID)
	}
}

// First key to contact a fresh server bootstraps as admin (key-path
// bootstrap parity with the tailnet path).
func TestKeyBootstrapAdminOnFreshServer(t *testing.T) {
	logs := captureLogs(t)
	e := newFreshTestEnv(t, nil)

	client, err := e.dialAs("ada", nil, ssh.PublicKeys(e.signer))
	if err != nil {
		t.Fatalf("fresh-server key dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	m := serverInfoMember(t, controlClientOn(t, client))
	if m.Role != string(domain.RoleAdmin) || m.Pending {
		t.Errorf("key bootstrap member = %+v, want approved admin", m)
	}
	if !strings.Contains(logs.String(), "bootstrap") {
		t.Errorf("bootstrap audit log missing: %q", logs.String())
	}

	// Second unknown key is rejected as before.
	var banner strings.Builder
	if c, err := e.dialAs("eve", &banner, ssh.PublicKeys(newSigner(t))); err == nil {
		_ = c.Close()
		t.Fatal("second unknown key accepted after bootstrap")
	}
	if !strings.Contains(banner.String(), "no Aether member for this key") {
		t.Errorf("banner = %q, want the unknown-key rejection", banner.String())
	}
}

// The publickey callback also fires for unsigned acceptability probes,
// so on a fresh server it must not create the admin row - an attacker
// could otherwise wedge bootstrap with a key nobody holds. The callback
// only tags the permissions; the store write happens post-handshake.
func TestKeyBootstrapProbeIsSideEffectFree(t *testing.T) {
	e := newFreshTestEnv(t, nil)
	key := newSigner(t).PublicKey()

	perms, err := e.srv.authenticate(nil, key)
	if err != nil {
		t.Fatalf("probe callback on fresh server: %v", err)
	}
	if perms.Extensions[memberIDExtension] != "" {
		t.Error("probe callback minted a member ID before signature proof")
	}
	if got := perms.Extensions[bootstrapKeyExtension]; got != string(ssh.MarshalAuthorizedKey(key)) {
		t.Errorf("bootstrap extension = %q, want the offered key", got)
	}
	members, err := e.store.ListMembers(context.Background())
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("probe created %d member(s); bootstrap is wedged", len(members))
	}

	// A signed handshake still bootstraps normally afterwards.
	client, err := e.dialAs("ada", nil, ssh.PublicKeys(e.signer))
	if err != nil {
		t.Fatalf("signed bootstrap dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if m := serverInfoMember(t, controlClientOn(t, client)); m.Role != string(domain.RoleAdmin) {
		t.Errorf("bootstrap role = %q, want admin", m.Role)
	}
}

// LocalWhoIs speaks the LocalAPI whois endpoint over a unix socket and
// maps person, tagged-node, and failure responses.
func TestLocalWhoIs(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "ts.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	responses := map[string]string{
		"100.1.1.1:22": `{"Node":{"StableID":"n1","Tags":null},"UserProfile":{"LoginName":"alice@example.com"}}`,
		"100.2.2.2:22": `{"Node":{"StableID":"n2","Tags":["tag:ci"]},"UserProfile":{"LoginName":"tagged-devices"}}`,
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/whois" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("proto") != "tcp" {
			http.Error(w, "missing proto=tcp", http.StatusBadRequest)
			return
		}
		body, ok := responses[r.URL.Query().Get("addr")]
		if !ok {
			http.Error(w, "no match for IP:port", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := NewLocalWhoIs(sock)

	id, err := w.WhoIs(ctx, "100.1.1.1:22")
	if err != nil {
		t.Fatalf("whois person: %v", err)
	}
	if id.Login != "alice@example.com" || id.NodeID != "n1" || id.Tagged {
		t.Errorf("person identity = %+v", id)
	}

	id, err = w.WhoIs(ctx, "100.2.2.2:22")
	if err != nil {
		t.Fatalf("whois tagged: %v", err)
	}
	if !id.Tagged || id.Login != "" || id.NodeID != "n2" {
		t.Errorf("tagged identity = %+v", id)
	}

	if _, err = w.WhoIs(ctx, "192.168.0.1:9"); err == nil {
		t.Error("off-tailnet address did not error")
	}

	dead := NewLocalWhoIs(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err = dead.WhoIs(ctx, "100.1.1.1:22"); err == nil {
		t.Error("missing socket did not error")
	}
}

// The wire Member shape stays pinned for approved members: pending only
// appears while it is true.
func TestMemberWireShapePendingOmitted(t *testing.T) {
	raw, err := json.Marshal(protocol.MemberFromDomain(&domain.Member{
		ID: "m_1", DisplayName: "Ada", Color: "#e6194b", Role: domain.RoleAdmin,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pending") {
		t.Errorf("approved member leaks pending onto the wire: %s", raw)
	}
	raw, _ = json.Marshal(protocol.MemberFromDomain(&domain.Member{
		ID: "m_2", DisplayName: "Bob", TailnetLogin: "bob@ts.net",
		Color: "#3cb44b", Role: domain.RoleCollaborator, Pending: true,
	}))
	if !strings.Contains(string(raw), `"pending":true`) {
		t.Errorf("pending member does not surface pending: %s", raw)
	}
}
