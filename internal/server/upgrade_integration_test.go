//go:build integration

package server

import (
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// mainSchemaVersion is the schema version of a database written by main,
// the last release without the edge: every migration before v45, the edge
// identities.
const mainSchemaVersion = 44

// mainMigrations reads the migrations an installation from main applied
// out of internal/store/migrate.go, which appends versions and never
// edits one that shipped.
func mainMigrations(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "internal", "store", "migrate.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var all []string
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "migrations" {
			return true
		}
		for _, elt := range spec.Values[0].(*ast.CompositeLit).Elts {
			s, uerr := strconv.Unquote(elt.(*ast.BasicLit).Value)
			if uerr != nil {
				t.Fatalf("migration %d: %v", len(all)+1, uerr)
			}
			all = append(all, s)
		}
		return false
	})
	if len(all) <= mainSchemaVersion || !strings.Contains(all[mainSchemaVersion], "CREATE TABLE member_identities") {
		t.Fatalf("%s has %d migrations and v%d is not the edge identities; update mainSchemaVersion", path, len(all), mainSchemaVersion+1)
	}
	return all[:mainSchemaVersion]
}

// seedMainDatabase writes the database an installation from main has at
// path: main's schema, a member who signs in with an SSH key, one who
// signs in with a tailnet identity, and rows of tables that reference
// members.
func seedMainDatabase(t *testing.T, path string, key ssh.PublicKey) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck // test database
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i, m := range mainMigrations(t) {
		if _, err := raw.Exec(m); err != nil {
			t.Fatalf("apply v%d: %v", i+1, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, tailnet_login, pending, color, role, created_at, image, git_name, git_email)
			VALUES ('m-key', 'Ada', ?, '', 0, '#e6194b', 'admin', 1, '', 'Ada L', 'ada@example.com'),
			       ('m-tailnet', 'Bo', '', 'bo@example.com', 0, '#3cb44b', 'collaborator', 2, '', '', '');
		INSERT INTO account_shares (owner_member_id, grantee_member_id, created_at) VALUES ('m-key', 'm-tailnet', 3);
		INSERT INTO harness_definitions (member_id, name, definition, created_at, updated_at) VALUES ('m-key', 'mine', '{}', 4, 4);
	`, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// tableRows counts the rows of every table of the database at path.
func tableRows(t *testing.T, path string) map[string]int {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck // test database
	rows, err := raw.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	_ = rows.Close()
	counts := map[string]int{}
	for _, table := range tables {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// refuseHTTP makes every HTTP request of this process fail and records
// it, until the test ends. The edge agent enrolls, and fetches an edge's
// key, through the default HTTP client, so an agent dialing any edge
// shows up here.
func refuseHTTP(t *testing.T) func() []string {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []string
	)
	saved := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		return nil, errors.New("this test allows no outbound HTTP")
	})
	t.Cleanup(func() { http.DefaultTransport = saved })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// An installation from main has no edge-url or edge-access in its
// configuration and a database at main's schema version. This build
// starts on both as that installation did: it opens no connection to any
// edge and writes no edge state, keeps every row through the edge
// identities migration, and its SSH key and tailnet members sign in as
// before.
func TestIntegrationUpgradeFromMainWithoutTheEdge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dataDir := filepath.Join(shortTempDir(t), "data")
	_, signer := writeClientKey(t)
	seedMainDatabase(t, StorePath(dataDir), signer.PublicKey())
	before := tableRows(t, StorePath(dataDir))
	requests := refuseHTTP(t)

	// The SSH key member connects from off the tailnet.
	whois := &stubWhoIs{}
	whois.set(sshd.WhoIsIdentity{}, errors.New("not a tailnet address"))
	// What `aether-server serve` passes for a configuration without the
	// edge keys (TestEdgeIsOffUnlessConfigured in cmd/aether-server).
	srv, err := New(ctx, Config{DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: newE2ERuntime(), WhoIs: whois,
		EdgeURL: "", EdgeAccess: edgeproto.PolicyApprovedDevices})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	if m := memberInfo(t, openControl(t, dialSSH(t, addr, signer))); m.ID != "m-key" || m.Role != string(domain.RoleAdmin) {
		t.Fatalf("the SSH key member signs in as %+v, want m-key, admin", m)
	}
	whois.set(sshd.WhoIsIdentity{Login: "bo@example.com", NodeID: "node-bo"}, nil)
	tailnet, err := dialNoAuth(addr, "bo", nil)
	if err != nil {
		t.Fatalf("the tailnet member's dial: %v", err)
	}
	t.Cleanup(func() { _ = tailnet.Close() })
	if m := memberInfo(t, openControl(t, tailnet)); m.ID != "m-tailnet" || m.Role != string(domain.RoleCollaborator) || m.Pending {
		t.Fatalf("the tailnet member signs in as %+v, want m-tailnet, collaborator", m)
	}

	// An agent starts dialing as the server runs; give one the time it
	// would need to show.
	time.Sleep(2 * time.Second)
	if got := requests(); len(got) != 0 {
		t.Fatalf("the server made outbound HTTP requests: %v", got)
	}
	if _, err := os.Stat(edgeagent.StateDir(dataDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the server wrote edge state: %v", err)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("server run: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("server close: %v", err)
	}

	after := tableRows(t, StorePath(dataDir))
	for table, n := range before {
		if table != "schema_migrations" && after[table] < n {
			t.Errorf("table %s has %d rows after the upgrade, %d before", table, after[table], n)
		}
	}
	for _, table := range []string{"members", "account_shares", "harness_definitions"} {
		if after[table] != before[table] {
			t.Errorf("table %s has %d rows after the upgrade, %d before", table, after[table], before[table])
		}
	}
	if after["schema_migrations"] <= mainSchemaVersion {
		t.Fatalf("schema at %d migrations after the upgrade, want past v%d", after["schema_migrations"], mainSchemaVersion)
	}
}
