package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/store"
)

// homeDiffNoise are host-home entries the vendor login did not create:
// tool staging, caches, and shell state. They never become credential
// paths; a definition claiming ~/.cache would sync garbage forever.
var homeDiffNoise = map[string]bool{
	".local":                    true,
	".cache":                    true,
	".config":                   false, // many agents keep real login state here
	".ash_history":              true,
	".bash_history":             true,
	".sh_history":               true,
	".profile":                  true,
	".bashrc":                   true,
	".viminfo":                  true,
	".lesshst":                  true,
	".wget-hsts":                true,
	".gitconfig":                true,
	".sudo_as_admin_successful": true,
}

// agentSetupProfile resolves the launch profile an agent-setup shell runs
// under. Shipped names use their registry profile (no member definition is
// ever stored for them); custom names build a provisional profile from the
// request's argv proposal so the shell mounts the right home.
func (s *Scheduler) agentSetupProfile(req domain.WorkspaceShellRequest) (harness.Profile, error) {
	name := req.Harness
	if name == "custom" || name == "fake" {
		return harness.Profile{}, fmt.Errorf("scheduler: %q is a reserved harness name", name)
	}
	// An administrator definition pins the name for everyone; the setup
	// shell must run under it, not under a member proposal that later
	// launches would ignore.
	if spec, ok := s.harnesses[name]; ok && spec.Executable != "" {
		return (harness.Definition{
			Name: name, TUIArgs: spec.TUIArgs, HeadlessArgs: spec.HeadlessArgs,
			Executable: spec.Executable, ProfileRoot: spec.ProfileRoot,
			CredentialPaths: spec.CredentialPaths, DenyNames: spec.DenyNames,
		}).Profile(), nil
	}
	if shipped, ok := harness.Lookup(name); ok {
		return shipped, nil
	}
	def, err := agentSetupDefinition(req, nil, "")
	if err != nil {
		return harness.Profile{}, err
	}
	return def.Profile(), nil
}

// agentSetupDefinition assembles the definition an agent-setup shell
// proposes: name-derived executable, request argv templates (defaulted when
// absent), and the credential paths discovered at exit time.
func agentSetupDefinition(req domain.WorkspaceShellRequest, credentialPaths []string, profileRoot string) (harness.Definition, error) {
	name := req.Harness
	tui := req.TUIArgs
	if len(tui) == 0 {
		tui = []string{name, harness.TaskPlaceholder}
	}
	headless := req.HeadlessArgs
	if len(headless) == 0 {
		headless = []string{name, "-p", harness.TaskPlaceholder}
	}
	def := harness.Definition{
		Name:            name,
		TUIArgs:         tui,
		HeadlessArgs:    headless,
		Executable:      name,
		ProfileRoot:     profileRoot,
		CredentialPaths: credentialPaths,
	}
	if err := harness.ValidateMemberDefinition(def); err != nil {
		return harness.Definition{}, fmt.Errorf("scheduler: agent definition: %w", err)
	}
	return def, nil
}

// registerAgentDefinition finishes a clean agent-setup exit for a custom
// name: diff the host-side home for what the login flow wrote, convert the
// surviving entries to container credential paths, and store the member
// definition. Shipped names store nothing.
func (s *Scheduler) registerAgentDefinition(ctx context.Context, member domain.MemberID, req domain.WorkspaceShellRequest, plan *EnvironmentPlan, conn io.Writer) error {
	if spec, ok := s.harnesses[req.Harness]; ok && spec.Executable != "" {
		_, _ = io.WriteString(conn, "aether: "+req.Harness+" is ready (administrator-defined agent; tools and login saved).\r\n")
		return nil
	}
	if _, shipped := harness.Lookup(req.Harness); shipped {
		_, _ = io.WriteString(conn, "aether: "+req.Harness+" is ready (shipped agent; tools and login saved).\r\n")
		return nil
	}
	hostHome := filepath.Join(s.cfg.HomesDir, string(member), req.Harness)
	credentialPaths, err := discoverCredentialPaths(hostHome, plan.Home)
	if err != nil {
		return fmt.Errorf("scheduler: discover credential paths: %w", err)
	}
	profileRoot := ""
	if len(credentialPaths) == 1 {
		profileRoot = credentialPaths[0]
	}
	def, err := agentSetupDefinition(req, credentialPaths, profileRoot)
	if err != nil {
		return err
	}
	blob, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("scheduler: encode agent definition: %w", err)
	}
	row := &store.HarnessDefinition{MemberID: member, Name: def.Name, Definition: blob}
	if err := s.cfg.Store.UpsertHarnessDefinition(ctx, row); err != nil {
		return fmt.Errorf("scheduler: store agent definition: %w", err)
	}
	if len(credentialPaths) > 0 {
		_, _ = io.WriteString(conn, "aether: registered agent "+def.Name+"; login state persists in "+strings.Join(credentialPaths, ", ")+"\r\n")
	} else {
		_, _ = io.WriteString(conn, "aether: registered agent "+def.Name+" (no login state detected; run aether agent add again if the agent needs a login)\r\n")
	}
	return nil
}

// seedStagingFromActiveSnapshot copies the member's active tool snapshot
// into a fresh staging directory so a completed shell promotes the union
// of old and new tools. Without the seed, installing agent B would evict
// agent A: the member+workspace has a single active head. No active
// snapshot seeds nothing.
func (s *Scheduler) seedStagingFromActiveSnapshot(ctx context.Context, member domain.MemberID, ws domain.WorkspaceID, staging string) error {
	active, err := s.cfg.Toolenv.ActivePath(ctx, member, ws)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scheduler: resolve active snapshot: %w", err)
	}
	if err := os.CopyFS(staging, os.DirFS(active)); err != nil {
		return fmt.Errorf("scheduler: seed staging from active snapshot: %w", err)
	}
	return nil
}

// acquireAgentSetup claims the member+harness agent-setup slot. Concurrent
// sessions would race the exit-time promotion and registration writes, so
// the second session is refused up front rather than corrupted later.
func (s *Scheduler) acquireAgentSetup(member domain.MemberID, name string) (func(), error) {
	key := string(member) + "/" + name
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentSetups == nil {
		s.agentSetups = make(map[string]struct{})
	}
	if _, busy := s.agentSetups[key]; busy {
		return nil, fmt.Errorf("scheduler: an agent setup for %q is already in progress; finish or disconnect it first", name)
	}
	s.agentSetups[key] = struct{}{}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.agentSetups, key)
	}, nil
}

// discoverCredentialPaths lists the top-level entries the login flow left
// in the member's host-side home, filters noise, and converts survivors to
// absolute container paths. Entries that fail the harness path validator
// (which confines paths to /root or /home/aether) abort registration: a
// silently dropped path would strand login state.
func discoverCredentialPaths(hostHome, containerHome string) ([]string, error) {
	entries, err := os.ReadDir(hostHome)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if homeDiffNoise[name] {
			continue
		}
		// A symlink here is member-authored and would be resolved on the
		// host at mount time; it must never become a credential path, or
		// it could alias another member's files under HomesDir.
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("login state %q is a symlink; symlinked credential paths are not supported", name)
		}
		containerPath := path.Join(containerHome, name)
		probe := harness.Definition{
			Name: "probe", Executable: "probe",
			TUIArgs: []string{"probe", harness.TaskPlaceholder}, HeadlessArgs: []string{"probe", harness.TaskPlaceholder},
			CredentialPaths: []string{containerPath},
		}
		if err := harness.ValidateMemberDefinition(probe); err != nil {
			return nil, fmt.Errorf("login state %q cannot be mounted: %w", name, err)
		}
		paths = append(paths, containerPath)
	}
	sort.Strings(paths)
	return paths, nil
}
