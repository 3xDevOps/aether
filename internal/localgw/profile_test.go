package localgw

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// profileHome points os.UserHomeDir at a scratch home holding one claude
// profile, and returns its root. The walk resolves the root through the
// real home directory - the package-private test hook lives in
// internal/cli/profile - so the home environment is what a gateway test
// can steer. USERPROFILE is set as well so os.UserHomeDir resolves on
// Windows too.
func profileHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	p, ok := harness.Lookup("claude")
	if !ok {
		t.Fatal("claude harness missing")
	}
	root := filepath.Join(home, filepath.FromSlash(p.LocalRoot))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// profilePreviewBody is the JSON shape of profile.preview's answer.
type profilePreviewBody struct {
	Harness string `json:"harness"`
	Root    string `json:"root"`
	Present bool   `json:"present"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Blocked bool   `json:"blocked"`

	Categories []struct {
		Name  string   `json:"category"`
		Files int      `json:"files"`
		Paths []string `json:"paths"`
	} `json:"categories"`
	Excluded []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	} `json:"excluded"`
}

// pushResultJSON is a canned profile.push answer. Only the digest varies
// across tests; the snapshot ID is the same one throughout.
func pushResultJSON(t *testing.T, digest string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(protocol.ProfilePushResult{
		Snapshot: protocol.ProfileSnapshot{ID: "psn_1", Harness: "claude", Digest: digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLocalProfilePreview(t *testing.T) {
	root := profileHome(t, map[string]string{
		"CLAUDE.md":           "# standing instructions\n",
		"skills/pdf/SKILL.md": "# pdf skill\n",
		".credentials.json":   `{"token":"x"}`,
	})
	backend := &verbStubBackend{}
	g := newVerbGateway(t, backend, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.preview", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("profile.preview = %d: %s", rec.Code, rec.Body)
	}
	var got profilePreviewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Harness != "claude" || got.Root != root || !got.Present || got.Blocked {
		t.Fatalf("preview = %+v", got)
	}
	if got.Files != 2 || got.Bytes != int64(len("# standing instructions\n")+len("# pdf skill\n")) {
		t.Fatalf("files/bytes = %d/%d", got.Files, got.Bytes)
	}
	if len(got.Categories) != 2 ||
		got.Categories[0].Name != "memory" || got.Categories[0].Paths[0] != "CLAUDE.md" ||
		got.Categories[1].Name != "skills" || got.Categories[1].Paths[0] != "skills/pdf/SKILL.md" {
		t.Fatalf("categories = %+v", got.Categories)
	}
	if len(got.Excluded) != 1 || got.Excluded[0].Path != ".credentials.json" ||
		got.Excluded[0].Reason != "credential" {
		t.Fatalf("excluded = %+v", got.Excluded)
	}
	// The preview is answered entirely from this machine.
	if calls := backend.recorded(); len(calls) != 0 {
		t.Fatalf("preview called the server: %+v", calls)
	}
}

// A harness with no profile root on this machine is a normal answer, not
// an error: the wizard marks it "nothing to import".
func TestLocalProfilePreviewMissingRoot(t *testing.T) {
	root := profileHome(t, nil)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.preview", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("profile.preview = %d: %s", rec.Code, rec.Body)
	}
	var got profilePreviewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Present || got.Files != 0 || len(got.Categories) != 0 {
		t.Fatalf("preview = %+v", got)
	}
}

// The dashboard has no --allow-secret: a finding refuses the push and
// names the file plus the terminal command that can override it, where
// --workspace makes the override attributable.
func TestLocalProfilePushRefusesASecret(t *testing.T) {
	secret, err := os.ReadFile(filepath.Join("..", "cli", "profile", "testdata", "embedded_token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	root := profileHome(t, map[string]string{
		"CLAUDE.md":      "# standing instructions\n",
		"memory/leak.md": string(secret),
	})
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodProfilePush: pushResultJSON(t, "d1")},
	}}
	g := newVerbGateway(t, backend, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.push", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	for _, want := range []string{
		"secret detected",
		"memory/leak.md",
		root,
		"aether profile push --agent claude --allow-secret memory/leak.md --workspace <workspace>",
	} {
		if !strings.Contains(perr.Message, want) {
			t.Errorf("message %q does not name %q", perr.Message, want)
		}
	}
	for _, call := range backend.recorded() {
		if call.method == protocol.MethodProfilePush {
			t.Fatalf("a blocked push still uploaded: %s", call.params)
		}
	}
}

func TestLocalProfilePush(t *testing.T) {
	profileHome(t, map[string]string{
		"CLAUDE.md":     "# standing instructions\n",
		"settings.json": `{"model":"opus"}`,
	})
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodProfilePush: pushResultJSON(t, "sha1")},
	}}
	g := newVerbGateway(t, backend, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.push", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("profile.push = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Harness    string `json:"harness"`
		SnapshotID string `json:"snapshot_id"`
		Digest     string `json:"digest"`
		Files      int    `json:"files"`
		Bytes      int64  `json:"bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Harness != "claude" || got.SnapshotID != "psn_1" || got.Digest != "sha1" {
		t.Fatalf("push = %+v", got)
	}
	if got.Files != 2 || got.Bytes != int64(len("# standing instructions\n")+len(`{"model":"opus"}`)) {
		t.Fatalf("files/bytes = %d/%d", got.Files, got.Bytes)
	}

	// The head is consulted first so the upload is a delta.
	calls := backend.recorded()
	if len(calls) != 2 || calls[0].method != protocol.MethodProfileStatus || calls[1].method != protocol.MethodProfilePush {
		t.Fatalf("backend calls = %+v", calls)
	}
	var params protocol.ProfilePushParams
	if err := json.Unmarshal([]byte(calls[1].params), &params); err != nil {
		t.Fatal(err)
	}
	if params.Harness != "claude" || params.WorkspaceID != "" || len(params.AllowSecret) != 0 {
		t.Fatalf("push params = %+v", params)
	}
	if len(params.Paths) != 2 || len(params.Blobs) != 2 {
		t.Fatalf("paths=%d blobs=%d, want the whole profile", len(params.Paths), len(params.Blobs))
	}
	// The gateway never builds an allow-secret override, so the key is
	// not on the wire at all.
	var wire map[string]json.RawMessage
	if err := json.Unmarshal([]byte(calls[1].params), &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["allow_secret"]; ok {
		t.Errorf("push carries allow_secret: %s", calls[1].params)
	}
	if _, ok := wire["workspace_id"]; ok {
		t.Errorf("push carries workspace_id: %s", calls[1].params)
	}
}

// A push sends only the blobs the server's head does not already carry,
// while still listing every path in the snapshot.
func TestLocalProfilePushSendsOnlyNewBlobs(t *testing.T) {
	kept := "# standing instructions\n"
	profileHome(t, map[string]string{
		"CLAUDE.md":     kept,
		"settings.json": `{"model":"opus"}`,
	})
	sum := sha256.Sum256([]byte(kept))
	keptDigest := hex.EncodeToString(sum[:])
	status, err := json.Marshal(protocol.ProfileStatusResult{
		Snapshot: &protocol.ProfileSnapshot{ID: "psn_0", Harness: "claude"},
		Files:    []protocol.ProfileFileMeta{{Path: "CLAUDE.md", Digest: keptDigest, Mode: 0o644}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{
			protocol.MethodProfileStatus: status,
			protocol.MethodProfilePush:   pushResultJSON(t, "sha2"),
		},
	}}
	g := newVerbGateway(t, backend, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.push", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("profile.push = %d: %s", rec.Code, rec.Body)
	}
	calls := backend.recorded()
	if len(calls) != 2 {
		t.Fatalf("backend calls = %+v", calls)
	}
	var params protocol.ProfilePushParams
	if err := json.Unmarshal([]byte(calls[1].params), &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Paths) != 2 {
		t.Fatalf("paths = %+v, want every file", params.Paths)
	}
	if len(params.Blobs) != 1 || params.Blobs[0].Digest == keptDigest {
		t.Fatalf("blobs = %d, want only the file the server lacks", len(params.Blobs))
	}
}

// A status failure is not fatal: the push falls back to sending every
// blob rather than refusing.
func TestLocalProfilePushSurvivesAStatusFailure(t *testing.T) {
	profileHome(t, map[string]string{"CLAUDE.md": "# standing instructions\n"})
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodProfilePush: pushResultJSON(t, "sha1")},
		errs: map[string]*protocol.Error{
			protocol.MethodProfileStatus: {Code: protocol.CodeUnavailable, Message: "server unreachable"},
		},
	}}
	g := newVerbGateway(t, backend, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.push", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("profile.push = %d: %s", rec.Code, rec.Body)
	}
	calls := backend.recorded()
	var params protocol.ProfilePushParams
	if err := json.Unmarshal([]byte(calls[len(calls)-1].params), &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Blobs) != 1 {
		t.Fatalf("blobs = %d, want the full upload", len(params.Blobs))
	}
}

// A refusal from the server is the server's word, passed through.
func TestLocalProfilePushSurfacesServerRefusal(t *testing.T) {
	profileHome(t, map[string]string{"CLAUDE.md": "# standing instructions\n"})
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		errs: map[string]*protocol.Error{
			protocol.MethodProfilePush: {Code: protocol.CodeDenied, Message: "profile sync is disabled"},
		},
	}}
	g := newVerbGateway(t, backend, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.push", `{"harness":"claude"}`, true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeDenied || perr.Message != "profile sync is disabled" {
		t.Fatalf("error = %+v", perr)
	}
}

func TestLocalProfileVerbParams(t *testing.T) {
	profileHome(t, map[string]string{"CLAUDE.md": "# standing instructions\n"})
	g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})

	for _, verb := range []string{"profile.preview", "profile.push"} {
		for _, body := range []string{`{}`, `{"harness":"ghost"}`} {
			rec := do(g, http.MethodPost, "/local/v1/"+verb, body, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d, want 400: %s", verb, body, rec.Code, rec.Body)
			}
			if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeInvalidParams {
				t.Errorf("%s %s code = %d, want %d", verb, body, perr.Code, protocol.CodeInvalidParams)
			}
		}
	}

	// profile.status is a server method, not a local verb.
	rec := do(g, http.MethodPost, "/local/v1/profile.status", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("profile.status = %d, want 404", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeMethodNotFound {
		t.Errorf("code = %d, want %d", perr.Code, protocol.CodeMethodNotFound)
	}
}

// TestLocalProfilePushReportsSkipped covers the size caps at the wire: an
// oversized file is not sent, the push still succeeds, and the answer says
// which file was left behind - the only place the caller learns it is not
// on the server.
func TestLocalProfilePushReportsSkipped(t *testing.T) {
	profileHome(t, map[string]string{
		"CLAUDE.md":           "# standing instructions\n",
		"projects/huge.jsonl": strings.Repeat("x", profilesvc.MaxFileBytes+1),
	})
	backend := &verbStubBackend{apiStubBackend: apiStubBackend{
		results: map[string]json.RawMessage{protocol.MethodProfilePush: pushResultJSON(t, "sha1")},
	}}
	g := newVerbGateway(t, backend, cli.Config{})

	rec := do(g, http.MethodPost, "/local/v1/profile.push", `{"harness":"claude"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("profile.push = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Files   int `json:"files"`
		Skipped []struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
			Detail string `json:"detail"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Files != 1 {
		t.Errorf("files = %d, want only the file under the cap", got.Files)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].Path != "projects/huge.jsonl" ||
		got.Skipped[0].Reason != profile.ExcludeTooLarge {
		t.Fatalf("skipped = %+v", got.Skipped)
	}
	// The oversized file never reaches the wire, not even as a path entry.
	for _, call := range backend.recorded() {
		if call.method == protocol.MethodProfilePush &&
			strings.Contains(call.params, "huge.jsonl") {
			t.Fatalf("the oversized file was sent: %s", call.params)
		}
	}
}
