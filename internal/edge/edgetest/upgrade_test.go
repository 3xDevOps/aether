//go:build edgeupgrade

package edgetest

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Base images the test pulls, pinned like every image this repository runs.
const (
	registryImage = "registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"
	nginxImage    = "nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de"
)

// qualifyVersion prefixes the versions of this tree's test builds, which
// are newer than any release so that the updater never refuses one as a
// downgrade.
const qualifyVersion = "v99.0.0-qualify."

// switchWait bounds how long servers take to reconnect after a switch.
const switchWait = 2 * time.Minute

// edgeImage is one aether-edge image in the test registry.
type edgeImage struct {
	version string
	digest  string
	// updater is false for an image from before version --json, which
	// the updater cannot update from. schema and floor are what its
	// version --json reports.
	updater       bool
	schema, floor int
}

// migrationStep is a migration a test build appends to the edge store's.
type migrationStep struct {
	floor int
	sql   string
}

// qualify is the host of TestEdgeUpgrade: a registry, a proxy network
// with a proxy container, the Compose directory of
// packaging/edge/compose.yaml and the updater's environment.
type qualify struct {
	t        *testing.T
	root     string
	work     string
	docker   string
	registry string
	network  string
	// front is the proxy's published address for the edge.
	front  string
	dir    string
	env    []string
	github *fakeGitHub
	h      *harness
	// fingerprint is the edge key's, which every release must keep.
	fingerprint string
}

// TestEdgeUpgrade qualifies an edge upgrade against real containers, as
// docs/testing.md, "Edge upgrade qualification", describes: the previous
// release with accounts and claimed servers, then scripts/edge-update.sh
// through a failed release, a release that migrates and fails, an
// additive migration and a rollback onto the migrated database, and the
// previous release's server on the new edge.
func TestEdgeUpgrade(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("TestEdgeUpgrade needs root: the edge's data directory belongs to uid 65532 (sudo make test-edge-upgrade)")
	}
	q := newQualify(t)
	prevTag := q.git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "HEAD^")
	worktree := filepath.Join(q.work, "previous")
	q.git("worktree", "add", "--detach", worktree, prevTag)
	t.Cleanup(func() { _, _ = q.run("git", "-C", q.root, "worktree", "remove", "--force", worktree) })
	peer := make(chan string, 1)
	go func() { peer <- q.buildPreviousServer(worktree) }()

	prev := q.build(prevTag, worktree)
	a := q.build(qualifyVersion+"1", q.root)
	failing := "INSERT INTO qualify_missing VALUES (1)"
	f := q.build(qualifyVersion+"2", q.patched(qualifyVersion+"2", migrationStep{a.schema + 1, failing}))
	mf := q.build(qualifyVersion+"3", q.patched(qualifyVersion+"3",
		migrationStep{a.schema + 1, "CREATE TABLE qualify_raised (id INTEGER PRIMARY KEY)"},
		migrationStep{a.schema + 2, failing}))
	m := q.build(qualifyVersion+"4", q.patched(qualifyVersion+"4",
		migrationStep{a.floor, "CREATE TABLE qualify_additive (id INTEGER PRIMARY KEY)"}))

	q.setDigest(prev.digest)
	q.compose("up", "-d", "--wait", "aether-edge")
	q.fingerprint = q.edgeFingerprint()
	t.Logf("%s runs with edge key %s", prevTag, q.fingerprint)
	q.h = &harness{t: t, github: q.github, signinURL: q.originFor("localhost"), relayURL: q.originFor("127.0.0.1")}
	clients := q.h.login(alice, bob)
	servers := []*serverNode{q.h.newServer(edgeproto.PolicyApprovedDevices), q.h.newServer(edgeproto.PolicyApprovedDevices)}
	for i, s := range servers {
		q.claim(clients[i], s.id, s.claimCode(t, time.Now()))
	}
	q.requireData(prev, clients, servers)

	q.promote(a)
	if prev.updater {
		q.update(0, "now running "+a.version)
	} else {
		q.update(1, "predates aether-edge version --json")
		q.requireData(prev, clients, servers)
		q.compose("stop", "aether-edge")
		q.mustRun("cp", "-a", filepath.Join(q.dir, "data"), filepath.Join(q.dir, "data.pre-update"))
		q.setDigest(a.digest)
		q.compose("up", "-d", "--wait", "aether-edge")
	}
	q.requireData(a, clients, servers)

	q.promote(f)
	out := q.update(1, "rolled back to "+a.version+" after "+f.version+" failed")
	if strings.Contains(out, "restoring") {
		t.Fatalf("%s left the database at schema %d; the rollback restored it anyway", f.version, a.schema)
	}
	q.requireFailed(f)
	q.requireData(a, clients, servers)
	q.requireSchema(a.schema)
	q.update(0, "skipping "+f.digest+", which failed before")

	q.promote(mf)
	q.update(1, "restoring /var/lib/aether-edge/backups/pre-update-"+mf.version+"-")
	q.requireFailed(mf)
	q.requireData(a, clients, servers)
	q.requireSchema(a.schema)

	before := q.backups(fmt.Sprintf("edge-v%d-v%d-*.db", a.schema, a.schema+1))
	q.promote(m)
	q.update(0, "now running "+m.version)
	after := q.backups(fmt.Sprintf("edge-v%d-v%d-*.db", a.schema, a.schema+1))
	if len(after) == 0 || slices.Contains(before, after[len(after)-1]) {
		t.Fatalf("%s migrated without a pre-migration backup: data/backups has %v, had %v", m.version, after, before)
	}
	if len(q.backups("pre-update-"+m.version+"-*.db")) != 1 {
		t.Fatalf("no updater backup pre-update-%s-*.db in data/backups", m.version)
	}
	q.requireData(m, clients, servers)
	q.requireSchema(a.schema + 1)

	q.previousServer(<-peer, worktree, clients[0])

	out = q.update(0, "now running "+a.version, "--rollback")
	if strings.Contains(out, "restoring") {
		t.Fatalf("the rollback restored a backup, but %s runs on the additive schema %d", a.version, a.schema+1)
	}
	q.requireFailed(m)
	q.requireData(a, clients, servers)
	q.requireSchema(a.schema + 1)
}

func newQualify(t *testing.T) *qualify {
	q := &qualify{t: t, work: t.TempDir(), github: newFakeGitHub(t)}
	var err error
	if q.docker, err = exec.LookPath("docker"); err != nil {
		t.Fatal(err)
	}
	q.root = q.mustRun("git", "rev-parse", "--show-toplevel")

	q.network = fmt.Sprintf("aether-edge-qualify-%d", os.Getpid())
	q.dockerRun("network", "create", q.network)
	t.Cleanup(func() { _, _ = q.run(q.docker, "network", "rm", q.network) })
	gateway := q.dockerRun("network", "inspect", "--format", "{{(index .IPAM.Config 0).Gateway}}", q.network)

	registry := q.network + "-registry"
	q.dockerRun("run", "-d", "--name", registry, "-p", "127.0.0.1::5000", registryImage)
	t.Cleanup(func() { _, _ = q.run(q.docker, "rm", "-f", "-v", registry) })
	q.registry = q.dockerRun("port", registry, "5000/tcp")
	t.Cleanup(func() {
		ids, _ := q.run(q.docker, "image", "ls", "-q", q.registry+"/aether-edge")
		for _, id := range strings.Fields(ids) {
			_, _ = q.run(q.docker, "image", "rm", "-f", id)
		}
	})

	// The edge reaches GitHub at fixed URLs: the proxy answers for
	// github.com and api.github.com and forwards to the fake on the host's
	// address on the network.
	ln, err := net.Listen("tcp", net.JoinHostPort(gateway, "0"))
	if err != nil {
		t.Fatal(err)
	}
	gh := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/oauth/access_token" {
			r.URL.Path = "/token"
		}
		q.github.srv.Config.Handler.ServeHTTP(w, r)
	})}
	go func() { _ = gh.Serve(ln) }()
	t.Cleanup(func() { _ = gh.Close() })

	proxyDir := filepath.Join(q.work, "proxy")
	q.mkdir(proxyDir, 0o755)
	// One certificate for the edge's two origins and GitHub's two hosts,
	// trusted by the edge, the updater's curl and this process.
	cert, key := selfSigned(t, "localhost", "127.0.0.1", "github.com", "api.github.com")
	q.write(filepath.Join(proxyDir, "cert.pem"), cert, 0o444)
	q.write(filepath.Join(proxyDir, "key.pem"), key, 0o400)
	ca := filepath.Join(q.work, "ca.pem")
	q.write(ca, cert, 0o444)
	// The system roots are read once, at the first TLS handshake.
	t.Setenv("SSL_CERT_FILE", ca)
	conf, err := os.ReadFile(filepath.Join("testdata", "upgrade", "nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	q.write(filepath.Join(proxyDir, "nginx.conf"), bytes.ReplaceAll(conf, []byte("@FAKE_GITHUB@"), []byte(ln.Addr().String())), 0o444)
	proxy := q.network + "-proxy"
	q.dockerRun("run", "-d", "--name", proxy, "--network", q.network,
		"--network-alias", "github.com", "--network-alias", "api.github.com",
		"-p", "127.0.0.1::8443", "-v", proxyDir+":/etc/qualify:ro",
		"-v", filepath.Join(proxyDir, "nginx.conf")+":/etc/nginx/nginx.conf:ro", nginxImage)
	t.Cleanup(func() { _, _ = q.run(q.docker, "rm", "-f", proxy) })
	q.front = q.dockerRun("port", proxy, "8443/tcp")
	proxyIP := q.dockerRun("inspect", "--format", "{{(index .NetworkSettings.Networks \""+q.network+"\").IPAddress}}", proxy)
	_, port, _ := net.SplitHostPort(q.front)

	// packaging/edge/compose.yaml as an operator deploys it, with what
	// differs here in compose.override.yaml, which Compose merges in.
	q.dir = filepath.Join(q.work, "aether-edge")
	q.mkdir(q.dir, 0o755)
	compose, err := os.ReadFile(filepath.Join(q.root, "packaging", "edge", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	q.write(filepath.Join(q.dir, "compose.yaml"), compose, 0o644)
	q.write(filepath.Join(q.dir, "compose.override.yaml"), fmt.Appendf(nil, `services:
  aether-edge:
    image: %s/aether-edge@${AETHER_EDGE_DIGEST:?set AETHER_EDGE_DIGEST=sha256:<digest> in .env}
    environment:
      AETHER_EDGE_SIGNIN_ORIGIN: https://localhost:%s
      AETHER_EDGE_RELAY_ORIGIN: https://127.0.0.1:%s
      AETHER_EDGE_TRUSTED_PROXIES: %s/32
      AETHER_EDGE_GITHUB_CLIENT_ID: fake-client-id
      SSL_CERT_FILE: /run/qualify/ca.pem
    volumes:
      - ./ca.pem:/run/qualify/ca.pem:ro
networks:
  proxy:
    name: %s
    external: true
`, q.registry, port, port, proxyIP, q.network), 0o644)
	q.write(filepath.Join(q.dir, "ca.pem"), cert, 0o444)
	q.write(filepath.Join(q.dir, "github-client-secret"), []byte("fake-client-secret\n"), 0o400)
	q.mkdir(filepath.Join(q.dir, "data"), 0o700)
	for _, p := range []string{"data", "github-client-secret"} {
		if err := os.Chown(filepath.Join(q.dir, p), 65532, 65532); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = q.run(q.docker, "compose", "--project-directory", q.dir, "--env-file", filepath.Join(q.dir, ".env"), "down", "-v")
	})

	bin := filepath.Join(q.work, "bin")
	q.mkdir(bin, 0o755)
	for _, stub := range []string{"docker", "cosign"} {
		data, err := os.ReadFile(filepath.Join("testdata", "upgrade", stub))
		if err != nil {
			t.Fatal(err)
		}
		q.write(filepath.Join(bin, stub), data, 0o755)
	}
	releases, promoted := filepath.Join(q.work, "releases"), filepath.Join(q.work, "promoted")
	q.mkdir(releases, 0o755)
	q.mkdir(promoted, 0o755)
	config := filepath.Join(q.work, "aether-edge-update.env")
	q.write(config, fmt.Appendf(nil, `AETHER_EDGE_UPDATE_RELAY_URL=https://%s
AETHER_EDGE_UPDATE_COMPOSE_DIR=%s
AETHER_EDGE_UPDATE_STATE=%s
AETHER_EDGE_UPDATE_PUBLIC_WAIT=60
`, q.front, q.dir, filepath.Join(q.work, "state")), 0o644)
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"aether-edge", proxy} {
				out, _ := q.run(q.docker, "logs", "--tail", "100", c)
				t.Logf("docker logs %s:\n%s", c, out)
			}
		}
	})
	q.env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AETHER_EDGE_UPDATE_CONFIG="+config, "CURL_CA_BUNDLE="+ca, "QUALIFY_DOCKER="+q.docker,
		"QUALIFY_IMAGE="+q.registry+"/aether-edge", "QUALIFY_RELEASES="+releases, "QUALIFY_PROMOTED="+promoted)
	return q
}

func (q *qualify) run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (q *qualify) mustRun(name string, args ...string) string {
	q.t.Helper()
	out, err := q.run(name, args...)
	if err != nil {
		q.t.Fatal(err)
	}
	return out
}

func (q *qualify) dockerRun(args ...string) string {
	q.t.Helper()
	return q.mustRun(q.docker, args...)
}

func (q *qualify) git(args ...string) string {
	q.t.Helper()
	return q.mustRun("git", append([]string{"-C", q.root}, args...)...)
}

func (q *qualify) compose(args ...string) {
	q.t.Helper()
	q.dockerRun(append([]string{"compose", "--project-directory", q.dir, "--env-file", filepath.Join(q.dir, ".env")}, args...)...)
}

func (q *qualify) write(path string, data []byte, mode os.FileMode) {
	q.t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		q.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		q.t.Fatal(err)
	}
}

func (q *qualify) mkdir(path string, mode os.FileMode) {
	q.t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		q.t.Fatal(err)
	}
}

// build builds images/edge/Dockerfile in context as version, labelled as
// a release labels it, and pushes it. Release version signs its digest.
func (q *qualify) build(version, context string) edgeImage {
	q.t.Helper()
	ref := q.registry + "/aether-edge:" + version
	start := time.Now()
	q.dockerRun("build", "-q", "-f", filepath.Join(context, "images", "edge", "Dockerfile"),
		"--build-arg", "VERSION="+version, "--build-arg", "COMMIT=qualify",
		"--label", "org.opencontainers.image.version="+version, "-t", ref, context)
	q.dockerRun("push", "-q", ref)
	img := edgeImage{version: version, digest: q.manifestDigest(version)}
	q.write(filepath.Join(q.work, "releases", version), []byte(img.digest), 0o644)
	var v struct {
		StoreSchema      int  `json:"store_schema"`
		StoreCompatFloor *int `json:"store_compat_floor"`
	}
	out, err := q.run(q.docker, "run", "--rm", "--network", "none", ref, "version", "--json")
	if err == nil && json.Unmarshal([]byte(out), &v) == nil && v.StoreCompatFloor != nil {
		img.updater, img.schema, img.floor = true, v.StoreSchema, *v.StoreCompatFloor
	}
	q.t.Logf("built %s@%s in %s (schema %d, compat floor %d)", version, img.digest, time.Since(start).Round(time.Second), img.schema, img.floor)
	return img
}

// patched copies what images/edge/Dockerfile builds from into a new
// context, with steps appended to the edge store's migrations.
func (q *qualify) patched(name string, steps ...migrationStep) string {
	q.t.Helper()
	dir := filepath.Join(q.work, "src-"+name)
	for _, p := range []string{"cmd/aether-edge", "internal", "images/edge"} {
		if err := os.CopyFS(filepath.Join(dir, p), os.DirFS(filepath.Join(q.root, p))); err != nil {
			q.t.Fatal(err)
		}
	}
	for _, p := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(q.root, p))
		if err != nil {
			q.t.Fatal(err)
		}
		q.write(filepath.Join(dir, p), data, 0o644)
	}
	var src strings.Builder
	src.WriteString("package edgestore\n\nfunc init() {\n\tmigrations = append(migrations,\n")
	for _, s := range steps {
		fmt.Fprintf(&src, "\t\tmigration{floor: %d, sql: %q},\n", s.floor, s.sql)
	}
	src.WriteString("\t)\n}\n")
	q.write(filepath.Join(dir, "internal", "edge", "store", "qualify_migrations.go"), []byte(src.String()), 0o644)
	return dir
}

const manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

func (q *qualify) manifest(tag string) (body []byte, mediaType, digest string) {
	q.t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+q.registry+"/v2/aether-edge/manifests/"+tag, nil)
	if err != nil {
		q.t.Fatal(err)
	}
	req.Header.Set("Accept", manifestTypes)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		q.t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // read in full
	body, err = io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		q.t.Fatalf("GET manifest %s: %s %v\n%s", tag, resp.Status, err, body)
	}
	return body, resp.Header.Get("Content-Type"), resp.Header.Get("Docker-Content-Digest")
}

func (q *qualify) manifestDigest(tag string) string {
	_, _, d := q.manifest(tag)
	return d
}

// promote moves stable to img's digest byte for byte, as promote-edge.yml
// does, and records the attestation it signs.
func (q *qualify) promote(img edgeImage) {
	q.t.Helper()
	body, mediaType, _ := q.manifest(img.version)
	req, err := http.NewRequest(http.MethodPut, "http://"+q.registry+"/v2/aether-edge/manifests/stable", bytes.NewReader(body))
	if err != nil {
		q.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mediaType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		q.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		q.t.Fatalf("PUT stable: %s", resp.Status)
	}
	if got := q.manifestDigest("stable"); got != img.digest {
		q.t.Fatalf("stable is %s after promoting %s", got, img.digest)
	}
	q.write(filepath.Join(q.work, "promoted", img.digest), []byte(img.version), 0o644)
}

func (q *qualify) setDigest(digest string) {
	q.write(filepath.Join(q.dir, ".env"), []byte("AETHER_EDGE_DIGEST="+digest+"\n"), 0o644)
}

// update runs scripts/edge-update.sh with args and wants exit status
// code and want in its output.
func (q *qualify) update(code int, want string, args ...string) string {
	q.t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(q.root, "scripts", "edge-update.sh")}, args...)...)
	cmd.Env = q.env
	start := time.Now()
	out, err := cmd.CombinedOutput()
	got := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		got = exit.ExitCode()
	case err != nil:
		q.t.Fatal(err)
	}
	q.t.Logf("aether-edge-update %s: exit %d in %s\n%s", strings.Join(args, " "), got, time.Since(start).Round(time.Second), out)
	if got != code || !strings.Contains(string(out), want) {
		q.t.Fatalf("aether-edge-update %s: exit %d, want %d with %q", strings.Join(args, " "), got, code, want)
	}
	return string(out)
}

func (q *qualify) edge(args ...string) string {
	q.t.Helper()
	return q.dockerRun(append([]string{"exec", "aether-edge", "aether-edge"}, args...)...)
}

func (q *qualify) originFor(host string) string {
	_, port, _ := net.SplitHostPort(q.front)
	return "https://" + host + ":" + port
}

// edgeFingerprint is the fingerprint /v1/edge serves through the proxy.
func (q *qualify) edgeFingerprint() string {
	q.t.Helper()
	req, err := http.NewRequest(http.MethodGet, q.originFor("127.0.0.1")+"/v1/edge", nil)
	if err != nil {
		q.t.Fatal(err)
	}
	req.Header.Set(edgeproto.HeaderVersion, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		q.t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // decoded
	var info struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Fingerprint == "" {
		q.t.Fatalf("GET /v1/edge: %s %v", resp.Status, err)
	}
	return info.Fingerprint
}

// claim makes c the owner of the server id with its claim code.
func (q *qualify) claim(c *client, id, code string) {
	q.t.Helper()
	res, err := q.h.claim(c, code)
	if err != nil || res.Config.ServerID != id || res.Info.Member.Role != string(domain.RoleAdmin) {
		q.t.Fatalf("%s claims %s: %+v %v", c.user.Login, id, res.Info.Member, err)
	}
}

// requireData checks that the edge runs img with the edge key, accounts
// and claimed servers it had, and that each owner reaches their server
// through it.
func (q *qualify) requireData(img edgeImage, clients []*client, servers []*serverNode) {
	q.t.Helper()
	if env, err := os.ReadFile(filepath.Join(q.dir, ".env")); err != nil || !strings.Contains(string(env), img.digest) {
		q.t.Fatalf(".env pins %q, want %s: %v", env, img.digest, err)
	}
	if v := q.edge("version"); !strings.Contains(v, img.version) {
		q.t.Fatalf("the edge reports %q, want %s", v, img.version)
	}
	if fp := q.edgeFingerprint(); fp != q.fingerprint {
		q.t.Fatalf("%s serves edge key %s, want %s", img.version, fp, q.fingerprint)
	}
	accounts, list := q.edge("accounts", "list"), q.edge("servers", "list")
	for i, c := range clients {
		if !strings.Contains(accounts, fmt.Sprintf("github:%d", c.user.ID)) {
			q.t.Fatalf("%s lost account %s:\n%s", img.version, c.user.Login, accounts)
		}
		if !strings.Contains(list, servers[i].id) {
			q.t.Fatalf("%s lost server %s:\n%s", img.version, servers[i].id, list)
		}
	}
	deadline := time.Now().Add(switchWait)
	for i, c := range clients {
		for {
			ctl, err := q.h.tryControl(c, servers[i])
			if err == nil {
				var info protocol.ServerInfoResult
				if err = ctl.Call(protocol.MethodServerInfo, struct{}{}, &info); err == nil && info.Member.Role != string(domain.RoleAdmin) {
					err = fmt.Errorf("member %+v", info.Member)
				}
			}
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				q.t.Fatalf("%s does not reach server %s through %s: %v", c.user.Login, servers[i].id, img.version, err)
			}
			time.Sleep(time.Second)
		}
	}
	if !img.updater {
		return
	}
	var rep struct {
		Status  string `json:"status"`
		Servers struct {
			Claimed int `json:"claimed"`
		} `json:"servers"`
	}
	out := q.edge("healthcheck", "--ready", "--json")
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Status != "ready" || rep.Servers.Claimed != len(servers) {
		q.t.Fatalf("%s: healthcheck --ready --json = %s, want ready with %d claimed servers", img.version, out, len(servers))
	}
}

func (q *qualify) requireSchema(want int) {
	q.t.Helper()
	var s struct {
		Schema int `json:"schema"`
	}
	out := q.edge("schema", "--json")
	if err := json.Unmarshal([]byte(out), &s); err != nil || s.Schema != want {
		q.t.Fatalf("schema --json = %s, want schema %d", out, want)
	}
}

func (q *qualify) requireFailed(img edgeImage) {
	q.t.Helper()
	data, err := os.ReadFile(filepath.Join(q.work, "state", "failed"))
	if err != nil || !strings.Contains(string(data), img.digest+" "+img.version+" ") {
		q.t.Fatalf("%s is not in the updater's failed list: %q %v", img.version, data, err)
	}
}

func (q *qualify) backups(pattern string) []string {
	q.t.Helper()
	found, err := filepath.Glob(filepath.Join(q.dir, "data", "backups", pattern))
	if err != nil {
		q.t.Fatal(err)
	}
	return found
}

// buildPreviousServer compiles TestPreviousServer against the previous
// release's edgetest package, in its worktree, and returns the test
// binary, or the error.
func (q *qualify) buildPreviousServer(worktree string) string {
	src, err := os.ReadFile(filepath.Join("testdata", "upgrade", "previous_server_test.go"))
	if err != nil {
		return "error: " + err.Error()
	}
	if err = os.WriteFile(filepath.Join(worktree, "internal", "edge", "edgetest", "zz_previous_server_test.go"), src, 0o644); err != nil {
		return "error: " + err.Error()
	}
	bin := filepath.Join(q.work, "previous-server.test")
	cmd := exec.Command("go", "test", "-c", "-tags", "edgeupgrade", "-o", bin, "./internal/edge/edgetest")
	cmd.Dir = worktree
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Sprintf("error: go test -c in %s: %v\n%s", worktree, err, out)
	}
	return bin
}

// previousServer runs the previous release's server against the edge,
// has c claim it and reach it through the edge.
func (q *qualify) previousServer(bin, worktree string, c *client) {
	q.t.Helper()
	if rest, ok := strings.CutPrefix(bin, "error: "); ok {
		q.t.Fatal(rest)
	}
	cmd := exec.Command(bin, "-test.run", "^TestPreviousServer$", "-test.v")
	cmd.Dir = filepath.Join(worktree, "internal", "edge", "edgetest")
	cmd.Env = append(os.Environ(), "AETHER_EDGE_QUALIFY_RELAY="+q.h.relayURL)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		q.t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		q.t.Fatal(err)
	}
	var stderr, log bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		q.t.Fatal(err)
	}
	enrolled, done := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "qualify-peer: "); ok {
				enrolled <- rest
			}
			fmt.Fprintln(&log, sc.Text())
		}
	}()
	var id, code string
	select {
	case line := <-enrolled:
		id, code, _ = strings.Cut(line, " ")
	case <-done:
	case <-time.After(switchWait):
	}
	if id == "" {
		_ = cmd.Process.Kill()
		<-done
		_ = cmd.Wait()
		q.t.Fatalf("the previous release's server did not enroll:\n%s%s", &log, &stderr)
	}
	q.claim(c, id, code)
	ctl := q.h.control(c, &serverNode{id: id})
	if info := call[protocol.ServerInfoResult](q.t, ctl, protocol.MethodServerInfo, struct{}{}); info.Member.Role != string(domain.RoleAdmin) {
		q.t.Fatalf("server.info on the previous release's server = %+v", info.Member)
	}
	_ = stdin.Close()
	<-done
	if err := cmd.Wait(); err != nil {
		q.t.Fatalf("the previous release's server: %v\n%s%s", err, &log, &stderr)
	}
	q.t.Logf("the previous release's server %s enrolled, was claimed and relayed", id)
}

// selfSigned returns a certificate for hosts, its own trust anchor, and
// its key, both PEM.
func selfSigned(t *testing.T, hosts ...string) (cert, key []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aether edge upgrade test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
