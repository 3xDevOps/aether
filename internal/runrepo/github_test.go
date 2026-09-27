package runrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// Native gh talks TLS to a stateful REST fixture. This exercises its real JSON,
// --jq, pagination, HTTP errors and POST encoding, not fake command echoes.
// No ambient GitHub login or external GitHub request is used.
func githubExec(t *testing.T, handler http.Handler) ExecFunc {
	t.Helper()
	if _, err := exec.LookPath("gh"); err != nil {
		t.Fatalf("native gh is required for repository behavior tests: %v", err)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	// httptest's certificate covers example.com. A CONNECT tunnel preserves
	// gh's real TLS/hostname verification without DNS edits or privileged ports.
	const hostname = "example.com"
	if err := server.Certificate().VerifyHostname(hostname); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != hostname+":443" {
			http.Error(w, "unexpected proxy destination", http.StatusBadGateway)
			return
		}
		upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", server.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.Close() }()
		client, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = client.Close() }()
		if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			t.Error(err)
			return
		}
		if err := buffered.Flush(); err != nil {
			t.Error(err)
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(upstream, buffered)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	certificate := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	environment := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") || strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "SSL_CERT_") || strings.HasSuffix(upper, "_PROXY") {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, "GH_ENTERPRISE_TOKEN=isolated-fixture-token", "GH_CONFIG_DIR="+configDir, "SSL_CERT_FILE="+certificate, "HTTPS_PROXY="+proxy.URL, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return func(ctx context.Context, _ runtime.ID, argv []string, dir string) (int, string, string, error) {
		args := append([]string(nil), argv...)
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--hostname" {
				args[i+1] = hostname
			}
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir, cmd.Env = dir, environment
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), stdout.String(), stderr.String(), nil
		}
		return 0, stdout.String(), stderr.String(), err
	}
}

type githubFixture struct {
	mu               sync.Mutex
	target           PRTarget
	head             string
	prs              []githubPR
	posts            int
	failCreate       bool
	persistOnFailure bool
	failReviews      bool
	comments         []map[string]any
	login            string
}

func (f *githubFixture) pull(number int, headRepo string) githubPR {
	pr := githubPR{Number: number, URL: "https://github.com/" + f.target.Repository + "/pull/" + strconv.Itoa(number), State: "open", Title: "Native pull request"}
	pr.Base.Ref, pr.Head.Ref, pr.Head.SHA = f.target.BaseBranch, f.target.HeadBranch, f.head
	pr.Base.Repo = &struct {
		FullName string `json:"full_name"`
	}{f.target.Repository}
	pr.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{headRepo}
	return pr
}

func (f *githubFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	writeJSON := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	fail := func(message string) {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(map[string]string{"message": message})
	}
	switch {
	case path == "/user":
		login := f.login
		if login == "" {
			login = "shared-bot"
		}
		writeJSON(map[string]string{"login": login})
	case path == "/repos/"+f.target.HeadRepository+"/git/ref/heads/"+f.target.HeadBranch:
		writeJSON(map[string]any{"object": map[string]string{"sha": f.head}})
	case path == "/repos/"+f.target.Repository+"/pulls" && r.Method == http.MethodPost:
		f.posts++
		var request struct {
			Title, Body, Base, Head string
			HeadRepo                string `json:"head_repo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			fail(err.Error())
			return
		}
		owner, repo, _ := strings.Cut(f.target.HeadRepository, "/")
		if request.Base != f.target.BaseBranch || request.Head != owner+":"+f.target.HeadBranch || request.HeadRepo != repo {
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(map[string]string{"message": "wrong explicit head/base selection"})
			return
		}
		pr := f.pull(42, f.target.HeadRepository)
		if !f.failCreate || f.persistOnFailure {
			f.prs = append(f.prs, pr)
		}
		if f.failCreate {
			fail("creation transport failed after admission")
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(pr)
	case path == "/repos/"+f.target.Repository+"/pulls":
		owner, _, _ := strings.Cut(f.target.HeadRepository, "/")
		if r.URL.Query().Get("base") != f.target.BaseBranch || r.URL.Query().Get("head") != owner+":"+f.target.HeadBranch {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(map[string]string{"message": "missing exact head/base query"})
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		if page > len(f.prs) {
			writeJSON([]githubPR{})
		} else {
			if page < len(f.prs) {
				query := r.URL.Query()
				query.Set("page", strconv.Itoa(page+1))
				w.Header().Set("Link", "<https://"+r.Host+r.URL.Path+"?"+query.Encode()+">; rel=\"next\"")
			}
			writeJSON(f.prs[page-1 : page])
		}
	case strings.HasSuffix(path, "/check-runs"):
		writeJSON(map[string]any{"check_runs": []map[string]string{{"name": "build", "status": "completed", "conclusion": "failure", "html_url": "https://github.com/check/1"}}})
	case strings.HasSuffix(path, "/statuses"):
		writeJSON([]map[string]string{{"context": "deploy", "state": "failure", "target_url": "https://example.test/deploy"}, {"context": "deploy", "state": "success", "target_url": "https://example.test/old"}})
	case strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/comments"):
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		start := (page - 1) * perPage
		if start >= len(f.comments) {
			writeJSON([]any{})
			return
		}
		end := min(start+perPage, len(f.comments))
		writeJSON(f.comments[start:end])
	case strings.HasSuffix(path, "/reviews"):
		if f.failReviews {
			fail("reviews permission temporarily unavailable")
			return
		}
		writeJSON([]map[string]any{{"id": 71, "user": map[string]string{"login": "reviewer"}, "body": "Please fix the race", "state": "CHANGES_REQUESTED", "submitted_at": "2026-09-25T10:00:00Z"}})
	case strings.HasSuffix(path, "/comments"):
		writeJSON([]map[string]any{{"id": 72, "pull_request_review_id": 71, "user": map[string]string{"login": "reviewer"}, "body": "Hold the native reference transaction", "path": "selected", "line": 3, "original_line": 2, "side": "RIGHT", "diff_hunk": "@@ -1 +1 @@", "commit_id": f.head, "html_url": "https://github.com/comment/72"}})
	default:
		w.WriteHeader(http.StatusNotFound)
		writeJSON(map[string]string{"message": "unexpected fixture endpoint " + path})
	}
}

func newGitHubFixture(head string) *githubFixture {
	return &githubFixture{head: head, target: PRTarget{Repository: "upstream/project", BaseBranch: "main", HeadRepository: "publisher/fork", HeadBranch: "review"}}
}

func TestPRDiscoveryUsesExactForkAndRejectsAmbiguity(t *testing.T) {
	s, run, expected := newRepo(t)
	f := newGitHubFixture(expected.Head)
	f.prs = []githubPR{f.pull(4, "publisher/different-fork"), f.pull(7, f.target.HeadRepository)}
	s.exec = githubExec(t, f)
	result, err := s.LookupPR(t.Context(), run, f.target)
	if err != nil || result.PullRequest == nil || result.PullRequest.Number != 7 || result.Identity != "shared-bot" {
		t.Fatalf("exact discovery=%+v err=%v", result, err)
	}
	f.mu.Lock()
	f.prs = append(f.prs, f.pull(8, f.target.HeadRepository))
	f.mu.Unlock()
	result, err = s.LookupPR(t.Context(), run, f.target)
	if err == nil || result.PullRequest != nil || !strings.Contains(err.Error(), "multiple open PRs") {
		t.Fatalf("ambiguous discovery=%+v err=%v", result, err)
	}
}

func TestUncertainCreateReconcilesReadOnlyAndNeverReplaysPost(t *testing.T) {
	s, run, expected := newRepo(t)
	f := newGitHubFixture(expected.Head)
	f.failCreate, f.persistOnFailure = true, true
	s.exec = githubExec(t, f)
	request := PRCreateRequest{Expected: expected, Target: f.target, Title: "Publish", Body: "Reviewed", ExpectedLogin: "shared-bot"}
	result, err := s.CreatePR(t.Context(), run, request)
	if err == nil || !result.Reconciled || result.CreationUncertain || result.Created || result.PullRequest == nil || result.PullRequest.Number != 42 || result.Output.ExitCode == 0 || !strings.Contains(result.Output.Stderr, "creation transport failed") {
		t.Fatalf("uncertain create=%+v err=%v", result, err)
	}
	result, err = s.CreatePR(t.Context(), run, request)
	if err != nil || result.Created || result.PullRequest == nil || result.PullRequest.Number != 42 {
		t.Fatalf("reconciled retry=%+v err=%v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatalf("replayed POST %d times", f.posts)
	}
}

func TestPRCreateFailureRetainsRealPushAndUncertainty(t *testing.T) {
	s, run, expected := newRepo(t)
	bare := t.TempDir()
	gitCmd(t, bare, "init", "--bare")
	gitCmd(t, run.WorkDir, "remote", "add", "publish", bare)
	push, err := s.Push(t.Context(), run, PushRequest{Expected: expected, Target: PushTarget{Remote: "publish", Repository: bare, HeadBranch: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	f := newGitHubFixture(expected.Head)
	f.failCreate = true
	s.exec = githubExec(t, f)
	created, err := s.CreatePR(t.Context(), run, PRCreateRequest{Expected: expected, Target: f.target, Title: "Publish"})
	if err == nil || !created.CreationUncertain || created.PullRequest != nil || !push.Pushed {
		t.Fatalf("push=%+v create=%+v err=%v", push, created, err)
	}
	if got := gitCmd(t, bare, "rev-parse", "refs/heads/review"); got != expected.Head {
		t.Fatalf("PR failure erased push: %s", got)
	}
}

func TestPRCreateRejectsRemoteMismatchIdentitySwitchAndRevokedMutation(t *testing.T) {
	for _, mode := range []string{"remote", "identity", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			s, run, expected := newRepo(t)
			f := newGitHubFixture(expected.Head)
			request := PRCreateRequest{Expected: expected, Target: f.target, Title: "Publish", ExpectedLogin: "shared-bot"}
			if mode == "remote" {
				f.head = strings.Repeat("b", 40)
			}
			if mode == "identity" {
				f.login = "different-account"
			}
			if mode == "revoked" {
				run.Authorize = func(_ context.Context, mutate bool) error {
					if mutate {
						return errors.New("account revoked")
					}
					return nil
				}
			}
			s.exec = githubExec(t, f)
			result, err := s.CreatePR(t.Context(), run, request)
			if err == nil || result.Created || result.CreationUncertain {
				t.Fatalf("unsafe create=%+v err=%v", result, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.posts != 0 {
				t.Fatalf("mutation despite %s: %d POSTs", mode, f.posts)
			}
		})
	}
}

func TestFeedbackIsTypedBoundedAndRetainsEarlierDataOnFailure(t *testing.T) {
	s, run, expected := newRepo(t)
	f := newGitHubFixture(expected.Head)
	f.prs = []githubPR{f.pull(42, f.target.HeadRepository)}
	for i := 1; i <= 10; i++ {
		f.comments = append(f.comments, map[string]any{"id": i, "user": map[string]string{"login": "commenter"}, "body": strings.Repeat("review ", 400), "html_url": "https://github.com/comment/" + strconv.Itoa(i), "created_at": "2026-09-25T10:00:00Z"})
	}
	s.exec = githubExec(t, f)
	result, err := s.PRFeedback(t.Context(), run, f.target, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Comments) != 2 || result.Comments[0].ID != "1" || result.Comments[0].Author != "commenter" || len(result.Comments[0].Body) != 1024 {
		t.Fatalf("bounded comments=%+v", result)
	}
	if len(result.Checks) != 2 || result.Checks[0].Name != "build" || result.Checks[0].Conclusion != "failure" || result.Checks[1].Conclusion != "failure" {
		t.Fatalf("current checks=%+v", result.Checks)
	}
	if len(result.Reviews) != 1 || result.Reviews[0].State != "CHANGES_REQUESTED" || result.Reviews[0].Body != "Please fix the race" {
		t.Fatalf("reviews=%+v", result.Reviews)
	}
	if len(result.ReviewComments) != 1 || result.ReviewComments[0].ReviewID != "71" || result.ReviewComments[0].Line == nil || *result.ReviewComments[0].Line != 3 || result.ReviewComments[0].CommitOID != expected.Head {
		t.Fatalf("inline comments=%+v", result.ReviewComments)
	}
	f.mu.Lock()
	f.failReviews = true
	f.mu.Unlock()
	result, err = s.PRFeedback(t.Context(), run, f.target, 2)
	if err == nil || !result.Truncated || len(result.Comments) != 2 || result.Output.ExitCode == 0 || !strings.Contains(result.Output.Stderr, "reviews permission temporarily unavailable") {
		t.Fatalf("partial feedback=%+v err=%v", result, err)
	}
}
