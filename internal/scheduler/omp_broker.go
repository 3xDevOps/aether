package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ompbroker"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	// ompBrokerPort is where omp in a session VM reaches the auth broker of
	// its account: 127.0.0.1:ompBrokerPort.
	ompBrokerPort = 47630
	// ompBrokerDir holds the session's broker socket inside its VM.
	ompBrokerDir = "/run/aether-omp"
	// ompTerminalPrefix starts the broker session of a member's environment
	// terminal, whose account is the member's own.
	ompTerminalPrefix = "terminal-"
)

// ompXDG are the XDG base directories omp keeps its databases under when
// each has an omp directory; every other program in the session reads the
// same values when the variables are unset.
var ompXDG = []struct{ env, dir string }{
	{"XDG_DATA_HOME", ".local/share"},
	{"XDG_STATE_HOME", ".local/state"},
	{"XDG_CACHE_HOME", ".cache"},
}

// ompShared are the home's omp directories of plain files that a session
// VM shares rather than keeping its own, mounted where omp looks for them
// in its XDG layout.
var ompShared = []struct{ home, xdg string }{
	{".omp/plugins", ".local/share/omp/plugins"},
	{".omp/agent/sessions", ".local/share/omp/sessions"},
	{".omp/agent/blobs", ".local/share/omp/blobs"},
}

// ompBrokerMount gives a session VM, or a member's environment terminal
// VM, its way to the auth broker of its account, and makes omp in it use the
// broker and the XDG layout, so it does not open the logins in
// ~/.omp/agent/agent.db that the broker holds open. An omp session always
// gets these variables; any other VM keeps one its workspace sets.
func (s *Scheduler) ompBrokerMount(member domain.MemberID, run *domain.Run, profile harness.Profile, home string, env map[string]string) (runtime.Mount, error) {
	session := ompTerminalPrefix + string(member)
	if run != nil {
		session = string(run.ID)
	}
	sock, err := s.ompFront.Listen(session)
	if err != nil {
		return runtime.Mount{}, fmt.Errorf("scheduler: %w", err)
	}
	set := func(key, value string) {
		if _, ok := env[key]; !ok || profile.LoginBroker {
			env[key] = value
		}
	}
	set("OMP_AUTH_BROKER_URL", fmt.Sprintf("http://127.0.0.1:%d", ompBrokerPort))
	// The session's socket is its identity; omp refuses a broker URL
	// without a token.
	set("OMP_AUTH_BROKER_TOKEN", "aether-session")
	set("OMP_AUTH_BROKER_SNAPSHOT_TTL_MS", "0")
	for _, x := range ompXDG {
		set(x.env, path.Join(home, x.dir))
		if err := s.cfg.Homes.PrepareLoginMountpoint(member, path.Join(x.dir, "omp"), true); err != nil {
			return runtime.Mount{}, fmt.Errorf("scheduler: %w", err)
		}
	}
	return runtime.Mount{HostPath: filepath.Dir(sock), ContainerPath: ompBrokerDir, ReadOnly: true, Port: ompBrokerPort}, nil
}

// ompSharedMounts keep omp's plugins, session files and their blobs in the
// home over a session's own omp data directory.
func (s *Scheduler) ompSharedMounts(member domain.MemberID, homePath, home string) ([]runtime.Mount, error) {
	mounts := make([]runtime.Mount, 0, len(ompShared))
	for _, d := range ompShared {
		if err := s.cfg.Homes.PrepareLoginMountpoint(member, d.home, true); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		mounts = append(mounts, runtime.Mount{HostPath: homePath, Subpath: d.home, ContainerPath: path.Join(home, d.xdg)})
	}
	return mounts, nil
}

// authorizeOMP names the account whose omp logins session may use now: a
// run's account while the run is open and its launcher may still launch on
// that account, or a terminal's member.
func (s *Scheduler) authorizeOMP(ctx context.Context, session string) (ompbroker.Account, error) {
	var account domain.MemberID
	shared := false
	if member, ok := strings.CutPrefix(session, ompTerminalPrefix); ok {
		m, err := s.cfg.Store.GetMember(ctx, domain.MemberID(member))
		if err != nil {
			return ompbroker.Account{}, fmt.Errorf("member %s: %w", member, err)
		}
		account = m.ID
	} else {
		run, err := s.cfg.Store.GetRun(ctx, domain.RunID(session))
		if err != nil {
			return ompbroker.Account{}, fmt.Errorf("session %s: %w", session, err)
		}
		if run.FinishedAt != nil {
			return ompbroker.Account{}, fmt.Errorf("session %s has ended", session)
		}
		revoked, err := s.accountRevoked(ctx, run)
		if err != nil {
			return ompbroker.Account{}, err
		}
		if revoked {
			return ompbroker.Account{}, fmt.Errorf("session %s may no longer use %s's account", session, run.AccountMember())
		}
		account = run.AccountMember()
		shared = account != runLauncher(run)
	}
	if err := s.cfg.Homes.PrepareLoginMountpoint(account, ".omp/agent", true); err != nil {
		return ompbroker.Account{}, err
	}
	home, err := s.cfg.Homes.Path(account)
	if err != nil {
		return ompbroker.Account{}, err
	}
	prepare := func(ctx context.Context) (int, int, error) { return s.prepareOMPBroker(ctx, account) }
	return ompbroker.Account{ID: string(account), Home: home, Shared: shared, Prepare: prepare}, nil
}

// ompBeforeBroker reports whether run is an omp session whose VM was made
// before the server ran omp's auth broker: no plan since gave it a broker
// socket, so omp in it opens ~/.omp/agent/agent.db itself.
func (s *Scheduler) ompBeforeBroker(run *domain.Run) bool {
	if s.ompFront == nil || run.Harness != "omp" {
		return false
	}
	_, err := os.Stat(filepath.Join(s.ompBrokerRoot(), string(run.ID)))
	return errors.Is(err, fs.ErrNotExist)
}

// prepareOMPBroker refuses to start member's broker while an omp session
// from before brokers that still has its VM, sleeping or shutting down
// included, uses member's account or home: the broker and that VM would
// open ~/.omp/agent/agent.db from two kernels. A session that has no VM yet
// gets its broker socket before its VM. It returns the uid:gid member's
// sessions run as.
func (s *Scheduler) prepareOMPBroker(ctx context.Context, member domain.MemberID) (int, int, error) {
	s.mu.Lock()
	var withVM []domain.RunID
	for id, entry := range s.runs {
		if entry.containerID != "" && !entry.retained {
			withVM = append(withVM, id)
		}
	}
	s.mu.Unlock()
	for _, id := range withVM {
		r, err := s.cfg.Store.GetRun(ctx, id)
		if err != nil {
			continue
		}
		if (r.AccountMember() == member || r.HomeMember() == member) && s.ompBeforeBroker(r) {
			return 0, 0, fmt.Errorf("omp session %s started before the server ran omp's auth broker and still has %s's ~/.omp/agent/agent.db open; close or kill it, then retry", r.ID, member)
		}
	}
	return s.sessionOwner(ctx, member)
}

// sessionOwner is the uid:gid member's sessions run as, from the image
// they run, which owns the files they write in the home.
func (s *Scheduler) sessionOwner(ctx context.Context, member domain.MemberID) (int, int, error) {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return 0, 0, err
	}
	omp, _ := harness.Lookup("omp")
	imageUser, err := s.resolveContainerUser(ctx, cmp.Or(m.Image, s.cfg.StandardImage), omp)
	if err != nil {
		return 0, 0, err
	}
	user := sessionUser(imageUser)
	uid, gid, _ := strings.Cut(user, ":")
	u, err := strconv.Atoi(uid)
	if err != nil {
		return 0, 0, fmt.Errorf("session user %q: %w", user, err)
	}
	g, err := strconv.Atoi(gid)
	if err != nil {
		return 0, 0, fmt.Errorf("session user %q: %w", user, err)
	}
	return u, g, nil
}

// ForgetOMPAccount stops the broker of a member who was removed or merged
// into another and closes their environment terminal's broker socket.
func (s *Scheduler) ForgetOMPAccount(ctx context.Context, member domain.MemberID) {
	if s.ompFront == nil {
		return
	}
	s.ompFront.Close(ompTerminalPrefix + string(member))
	if err := s.cfg.OMPBrokers.Stop(ctx, string(member)); err != nil {
		slog.Warn("scheduler: stop the omp broker of a removed member", "member", member, "error", err)
	}
}

// ompBrokerRoot holds each session's broker socket in a directory of its
// own that only root can write, which a session VM's monitor trusts.
func (s *Scheduler) ompBrokerRoot() string {
	return filepath.Join(s.cfg.StateDir, "omp-broker")
}

// useOMPBroker serves session VMs' omp logins from brokers when the server
// runs sessions in VMs; Docker sessions open the home's logins directly.
func (s *Scheduler) useOMPBroker(brokers *ompbroker.Manager) {
	if brokers == nil || !s.sessionVMs() {
		return
	}
	s.ompFront = ompbroker.NewFront(brokers, s.ompBrokerRoot(), s.authorizeOMP)
	if err := s.ompFront.Restore(); err != nil {
		slog.Warn("scheduler: listen again on omp broker sockets", "error", err)
	}
}
