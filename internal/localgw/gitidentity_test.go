package localgw

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
)

// isolateGitConfig points git at a scratch global config and no system
// config, and moves out of any repository, so the verb answers what the
// test wrote rather than the developer's own identity.
func isolateGitConfig(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	global := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(global, []byte(contents), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Chdir(dir)
}

func TestLocalGitIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		config    string
		wantName  string
		wantEmail string
	}{
		{
			name:      "configured",
			config:    "[user]\n\tname = Ada Lovelace\n\temail = ada@example.invalid\n",
			wantName:  "Ada Lovelace",
			wantEmail: "ada@example.invalid",
		},
		{name: "unset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateGitConfig(t, tc.config)
			g := newVerbGateway(t, &verbStubBackend{}, cli.Config{})
			defer func() { _ = g.Close() }()

			rec := do(g, http.MethodPost, "/local/v1/git.identity", "", true)
			if rec.Code != http.StatusOK {
				t.Fatalf("git.identity = %d: %s", rec.Code, rec.Body)
			}
			var got struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Name != tc.wantName || got.Email != tc.wantEmail {
				t.Errorf("git.identity = %q/%q, want %q/%q", got.Name, got.Email, tc.wantName, tc.wantEmail)
			}
		})
	}
}
