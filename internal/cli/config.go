// Package cli is the aether command's SSH client: it dials the linked
// server, opens the control and attach/setup subsystems, and stores the
// local linked-server config.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Config is the local linked-server configuration written by `aether link`.
type Config struct {
	Addr string `json:"addr"`
	User string `json:"user,omitempty"`
	// Key is the private-key file chosen with `aether link --key`. Empty
	// means automatic discovery: the SSH agent, then the default files
	// under ~/.ssh (see ResolveAuth). `--key auto` clears it. Only
	// Aether's own SSH connections use it; git commands run the system
	// git over OpenSSH, which picks its own keys.
	Key string `json:"key,omitempty"`
	// Repo is the clone linked last, the one the dashboard's sync, push
	// and pull use. A config written before Repos existed has only this.
	Repo string `json:"repo,omitempty"`
	// Repos are the linked clones, one per workspace, across every server
	// this computer links: workspace ids are unique, so no link owns them
	// and relinking keeps them.
	Repos      []LinkedRepo `json:"repos,omitempty"`
	KnownHosts string       `json:"known_hosts,omitempty"`
	// EdgeURL and ServerID link the server through an edge. With ServerID
	// set, the host key must derive it on every path, Addr (when set) is
	// dialed before the edge, and the device key authenticates.
	EdgeURL  string `json:"edge_url,omitempty"`
	ServerID string `json:"server_id,omitempty"`
	// Links are the named server profiles saved by `aether link --name`.
	// The top-level fields stay the default link, so config files written
	// before this field existed parse and re-save unchanged.
	Links []NamedLink `json:"links,omitempty"`
	// GUIServer is the named link `aether gui` serves when started without
	// --server; the dashboard's server switch sets it, empty for the
	// default link.
	GUIServer string `json:"gui_server,omitempty"`
	// Active is the profile name this process runs on, set by Named and
	// never persisted: the file's top-level fields are always the default
	// link, whichever profile a given `aether gui --server` serves.
	Active string `json:"-"`
}

type LinkedRepo struct {
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
}

// WithRepo returns cfg with path recorded as the linked clone of
// workspaceID. A clone has one aether remote, so the entry replaces any
// other for that workspace or that path. cfg's own Repos slice is never
// mutated.
func WithRepo(cfg Config, workspaceID, path string) Config {
	repos := slices.DeleteFunc(slices.Clone(cfg.Repos), func(r LinkedRepo) bool {
		return r.WorkspaceID == workspaceID || r.Path == path
	})
	cfg.Repos = append(repos, LinkedRepo{WorkspaceID: workspaceID, Path: path})
	return cfg
}

// NamedLink is one saved server profile. Empty fields fall back to the
// top-level defaults when the profile is selected.
type NamedLink struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	User string `json:"user,omitempty"`
	// Key is the private-key file this profile was linked with. Empty
	// without AutoKey is a profile written before `aether link` recorded
	// a key choice; it inherits the top-level Key like every other empty
	// field, so those files keep working unchanged.
	Key string `json:"key,omitempty"`
	// AutoKey marks a profile linked by automatic discovery (no --key, or
	// --key auto). Named then ignores the top-level Key: an explicit
	// default-link key would narrow this profile to one identity its
	// server may not know.
	AutoKey    bool   `json:"auto_key,omitempty"`
	Repo       string `json:"repo,omitempty"`
	KnownHosts string `json:"known_hosts,omitempty"`
	// EdgeURL and ServerID are this profile's own and never inherited: a
	// server id pins one server, so borrowing the default link's would
	// refuse this profile's server, and an edge profile does not borrow
	// the default link's address either.
	EdgeURL  string `json:"edge_url,omitempty"`
	ServerID string `json:"server_id,omitempty"`
}

// Named returns c with the named link's non-empty fields overlaid on the
// top-level defaults, and Active set to the name. The second result is
// false when no link carries that name.
func (c Config) Named(name string) (Config, bool) {
	for _, l := range c.Links {
		if l.Name != name {
			continue
		}
		out := c
		out.Active = name
		if l.Addr != "" {
			out.Addr = l.Addr
		}
		if l.User != "" {
			out.User = l.User
		}
		switch {
		case l.AutoKey:
			out.Key = ""
		case l.Key != "":
			out.Key = l.Key
		}
		if l.Repo != "" {
			out.Repo = l.Repo
		}
		if l.KnownHosts != "" {
			out.KnownHosts = l.KnownHosts
		}
		out.EdgeURL, out.ServerID = l.EdgeURL, l.ServerID
		if l.ServerID != "" {
			out.Addr = l.Addr
		}
		return out, true
	}
	return Config{}, false
}

// UpsertLink returns cfg with l added to Links, replacing any existing
// entry of the same name. cfg's own Links slice is never mutated.
func UpsertLink(cfg Config, l NamedLink) Config {
	links := make([]NamedLink, len(cfg.Links), len(cfg.Links)+1)
	copy(links, cfg.Links)
	for i := range links {
		if links[i].Name == l.Name {
			links[i] = l
			cfg.Links = links
			return cfg
		}
	}
	cfg.Links = append(links, l)
	return cfg
}

// ByServerID returns the link, default or named, whose server id is id.
func (c Config) ByServerID(id string) (Config, bool) {
	if c.ServerID == id {
		return c, true
	}
	for _, l := range c.Links {
		if l.ServerID == id {
			return c.Named(l.Name)
		}
	}
	return Config{}, false
}

// GitHost is the host a git remote URL names for this link: the logical
// edge host when the link has a server id, so git reaches it through
// aether edge-ssh, and the address otherwise.
func (c Config) GitHost() string {
	if c.ServerID != "" {
		return EdgeHost(c.ServerID)
	}
	return c.Addr
}

// MovedGitURL is rawURL, a git remote of the link prev, on the git host
// of c. The second result is false when rawURL is not a remote of prev
// or c names the same host: a link that gains a server id moves its
// remotes from the address to the logical edge host.
func (c Config) MovedGitURL(prev Config, rawURL string) (string, bool) {
	if c.GitHost() == prev.GitHost() {
		return "", false
	}
	workspace, ok := strings.CutPrefix(rawURL, strings.TrimSuffix(GitURL(prev.User, prev.GitHost(), ""), ".git"))
	if !ok {
		return "", false
	}
	return GitURL(c.User, c.GitHost(), strings.TrimSuffix(workspace, ".git")), true
}

func (c Config) user() string {
	if c.User != "" {
		return c.User
	}
	return "aether"
}

func (c Config) knownHostsPath() string {
	if c.KnownHosts != "" {
		return c.KnownHosts
	}
	return defaultPath(".ssh", "known_hosts")
}

// ConfigDirEnv names the directory that holds config.json when set,
// ahead of the platform lookup. It is one variable on every platform,
// where os.UserConfigDir reads HOME on macOS, XDG_CONFIG_HOME on Linux,
// and AppData on Windows; tests set it so the file they write never
// depends on which of those their platform consults.
const ConfigDirEnv = "AETHER_CONFIG_DIR"

// Path is the linked-server config file: $AETHER_CONFIG_DIR/aether/config.json
// when that variable is set, otherwise ~/.config/aether/config.json on
// Linux, ~/Library/Application Support/aether/config.json on macOS, and
// %AppData%\aether\config.json on Windows.
func Path() (string, error) {
	dir := os.Getenv(ConfigDirEnv)
	if dir == "" {
		var err error
		if dir, err = os.UserConfigDir(); err != nil {
			return "", fmt.Errorf("cli: config dir: %w", err)
		}
	}
	return filepath.Join(dir, "aether", "config.json"), nil
}

// Dir is the directory that holds config.json, the device key and the
// device tokens.
func Dir() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

// ErrNotLinked reports that no local server link has been saved.
var ErrNotLinked = errors.New("not linked; run aether link <host:port>, or aether login and then aether link --claim <code>")

// Load reads the linked-server config. Missing files surface as a
// "not linked" error.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, ErrNotLinked
		}
		return Config{}, fmt.Errorf("cli: read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return Config{}, fmt.Errorf("cli: parse config: %w", err)
	}
	if cfg.Addr == "" && cfg.ServerID == "" {
		return Config{}, fmt.Errorf("cli: config %s has no addr", path)
	}
	return cfg, nil
}

// Save writes cfg to the linked-server config path (directory 0700, file 0600).
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cli: create config dir: %w", err)
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("cli: write config: %w", err)
	}
	return nil
}

func defaultPath(elems ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{home}, elems...)...)
}
