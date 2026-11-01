package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// assistantSignIns are the sign-in files of the assistant's agents, the only
// entries of its home a New conversation keeps.
var assistantSignIns = map[string]string{".claude": ".credentials.json", ".codex": "auth.json"}

// assistantSessions are the entries of each agent's directory that resuming
// its session reads, which a start keeps besides the sign-in.
var assistantSessions = map[string]func(name string, dir bool) bool{
	".claude": func(name string, dir bool) bool { return name == "projects" && dir },
	// Codex resumes from the rollout files alone. Its sqlite files hold
	// memories, dynamic tools and config imports.
	".codex": func(name string, dir bool) bool { return dir && (name == "sessions" || name == "archived_sessions") },
}

// maxSignInBytes caps a sign-in file the server reads; a real one is a few
// KiB.
const maxSignInBytes = 64 << 10

// readSignInFile reads the regular file rel in root, up to maxSignInBytes.
func readSignInFile(root *os.Root, rel string) ([]byte, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	if info.Size() > maxSignInBytes {
		return nil, fmt.Errorf("%s is larger than %d KiB", rel, maxSignInBytes>>10)
	}
	return root.ReadFile(rel)
}

// assistantLoginKeys are the keys each sign-in file may hold, with the keys
// allowed inside an object value; a scalar key has none. Claude Code also
// reads a gateway URL, MCP tokens and plugin secrets from its file, and Codex
// other auth modes from its own.
var assistantLoginKeys = map[string]map[string][]string{
	".claude/.credentials.json": {
		"claudeAiOauth": {"accessToken", "refreshToken", "expiresAt", "refreshTokenExpiresAt", "scopes", "subscriptionType", "rateLimitTier", "clientId"},
	},
	".codex/auth.json": {
		"auth_mode":      nil,
		"OPENAI_API_KEY": nil,
		"tokens":         {"id_token", "access_token", "refresh_token", "account_id"},
		"last_refresh":   nil,
	},
}

var codexAuthModes = []string{`"chatgpt"`, `"apikey"`}

// sweepAssistantHome deletes everything in the assistant home but each
// agent's sign-in file and, with sessions, what assistantSessions keeps. Only
// a regular file counts as a sign-in. The assistant container must not be
// running an agent.
func (s *Scheduler) sweepAssistantHome(ctx context.Context, home string, sessions bool) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("scheduler: create the assistant home: %w", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return fmt.Errorf("scheduler: read the assistant home: %w", err)
	}
	for _, e := range entries {
		signIn, ok := assistantSignIns[e.Name()]
		if !ok || !e.IsDir() {
			if err := s.removeAssistantEntry(ctx, home, e); err != nil {
				return err
			}
			continue
		}
		dir := filepath.Join(home, e.Name())
		inner, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("scheduler: read the assistant's %s: %w", e.Name(), err)
		}
		for _, f := range inner {
			if f.Name() == signIn && f.Type().IsRegular() || sessions && assistantSessions[e.Name()](f.Name(), f.IsDir()) && (f.IsDir() || f.Type().IsRegular()) {
				continue
			}
			if err := s.removeAssistantEntry(ctx, dir, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepareAssistantHome sweeps the assistant home for a start, rewrites each
// sign-in file to assistantLoginKeys and writes Claude Code's onboarding
// state. It reports what it removed from the sign-in files.
func (s *Scheduler) prepareAssistantHome(ctx context.Context, member domain.MemberID) ([]string, error) {
	home := AssistantHomeDir(s.cfg.AssistantDir, member)
	if err := s.sweepAssistantHome(ctx, home, true); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, fmt.Errorf("scheduler: open the assistant home: %w", err)
	}
	defer func() { _ = root.Close() }()
	var removed []string
	for _, rel := range slices.Sorted(maps.Keys(assistantLoginKeys)) {
		dropped, err := sanitizeAssistantLogin(root, rel, assistantLoginKeys[rel])
		if err != nil {
			return nil, err
		}
		for _, key := range dropped {
			removed = append(removed, rel+": "+key)
		}
	}
	claude, _ := harness.Lookup("claude")
	for rel, keys := range claude.BorrowedState {
		data, err := json.Marshal(keys)
		if err != nil {
			return nil, fmt.Errorf("scheduler: encode %s: %w", rel, err)
		}
		if err := root.WriteFile(rel, data, 0o600); err != nil {
			return nil, fmt.Errorf("scheduler: write the assistant's %s: %w", rel, err)
		}
	}
	return removed, nil
}

// removeAssistantEntry deletes e in dir. Only a real directory reaches
// s.cfg.RemoveAll, which may empty it as root.
func (s *Scheduler) removeAssistantEntry(ctx context.Context, dir string, e fs.DirEntry) error {
	p := filepath.Join(dir, e.Name())
	var err error
	if e.IsDir() {
		err = s.cfg.RemoveAll(ctx, p)
	} else {
		err = os.Remove(p)
	}
	if err != nil {
		return fmt.Errorf("scheduler: delete the assistant's %s: %w", e.Name(), err)
	}
	return nil
}

// sanitizeAssistantLogin rewrites the sign-in file rel to the keys allow
// permits and returns what it dropped. A file that is not a JSON object is
// deleted.
func sanitizeAssistantLogin(root *os.Root, rel string, allow map[string][]string) ([]string, error) {
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scheduler: inspect the assistant's %s: %w", rel, err)
	}
	if info.Size() > maxSignInBytes {
		if err = root.Remove(rel); err != nil {
			return nil, fmt.Errorf("scheduler: delete the assistant's %s: %w", rel, err)
		}
		return []string{fmt.Sprintf("the whole file, larger than %d KiB", maxSignInBytes>>10)}, nil
	}
	data, err := root.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("scheduler: read the assistant's %s: %w", rel, err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		if err = root.Remove(rel); err != nil {
			return nil, fmt.Errorf("scheduler: delete the assistant's %s: %w", rel, err)
		}
		return []string{"the whole file, which is not a JSON object"}, nil
	}
	var dropped []string
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		inner, ok := allow[key]
		switch {
		case !ok:
			dropped = append(dropped, key)
			delete(fields, key)
		case key == "auth_mode" && !slices.Contains(codexAuthModes, string(fields[key])):
			dropped = append(dropped, "auth_mode "+string(fields[key]))
			delete(fields, key)
		case inner != nil:
			var obj map[string]json.RawMessage
			if json.Unmarshal(fields[key], &obj) != nil || obj == nil {
				dropped = append(dropped, key)
				delete(fields, key)
				continue
			}
			for name := range obj {
				if !slices.Contains(inner, name) {
					dropped = append(dropped, key+"."+name)
					delete(obj, name)
				}
			}
			if fields[key], err = json.Marshal(obj); err != nil {
				return nil, fmt.Errorf("scheduler: encode the assistant's %s: %w", rel, err)
			}
		}
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	slices.Sort(dropped)
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("scheduler: encode the assistant's %s: %w", rel, err)
	}
	if err := root.WriteFile(rel, out, 0o600); err != nil {
		return nil, fmt.Errorf("scheduler: rewrite the assistant's %s: %w", rel, err)
	}
	return dropped, nil
}

// AssistantLoginFound reports whether agent is signed in for the assistant:
// a non-empty sign-in file in the assistant home from a sign-in the member
// made. Whose account it holds is checked at the next start. A sign-in in
// the member's own home does not count; their runs can write it.
func (s *Scheduler) AssistantLoginFound(member domain.MemberID, agent string) (bool, error) {
	profile, ok := harness.Lookup(agent)
	if !ok {
		return false, fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	rels, err := profile.LoginPaths()
	if err != nil {
		return false, fmt.Errorf("scheduler: %w", err)
	}
	root, err := os.OpenRoot(AssistantHomeDir(s.cfg.AssistantDir, member))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scheduler: open the assistant home: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, rel := range rels {
		info, err := root.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("scheduler: inspect the assistant's %s: %w", rel, err)
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			return s.assistantSignedIn(member, agent)
		}
	}
	return false, nil
}
