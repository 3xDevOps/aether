package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

// RunHostSuffix turns a run id into the host name ssh reaches it by:
// run-0123456789.aether. A run id is already a valid lowercase host label,
// so the name survives editors' host pickers and deep links unchanged.
const RunHostSuffix = ".aether"

// runHostKeyAlias is the one known_hosts name every run host checks its key
// under. Runs share the server's host key, so there is one entry per server
// and never a prompt per run.
const runHostKeyAlias = "aether"

// The two files Aether keeps in ~/.ssh, beside the user's own config.
const (
	runHostsFile      = "aether_config"
	runKnownHostsFile = "aether_known_hosts"
)

var errSubsystemRefused = errors.New("request refused")

// RunSSH opens the SSH transport into a run's container. The stream carries
// the SSH protocol to a server that presents hostKey, an authorized_keys
// line. A refusal is the server's own reason.
func (c *Conn) RunSSH(runID string) (io.ReadWriteCloser, string, error) {
	var ack protocol.RunSSHResponse
	out, err := c.openStream(protocol.SubsystemRunSSH, nil, protocol.RunSSHRequest{RunID: runID}, "ssh", &ack)
	if errors.Is(err, errSubsystemRefused) {
		return nil, "", errors.New("the server does not support ssh into a run; update aether-server")
	}
	if err != nil {
		return nil, "", err
	}
	if !ack.OK {
		_ = out.Close()
		return nil, "", errors.New(ack.Error)
	}
	return out, ack.HostKey, nil
}

// SSHOption is one ssh_config keyword and its value.
type SSHOption struct{ Key, Value string }

// RunSSHOptions are the ssh options under which a <run>.aether host reaches
// its run: this binary carries the connection, and the host key is checked
// against the file TrustRunHostKey maintains.
func RunSSHOptions() ([]SSHOption, error) {
	exe, err := selfPath()
	if err != nil {
		return nil, err
	}
	dir, err := sshDir()
	if err != nil {
		return nil, err
	}
	command := shellquote.Quote(exe)
	if runtime.GOOS == "windows" {
		command = `"` + exe + `"`
	}
	return []SSHOption{
		// %% is a literal percent sign to ssh; %h is the host name.
		{"ProxyCommand", strings.ReplaceAll(command, "%", "%%") + " ssh --stdio %h"},
		{"UserKnownHostsFile", sshConfigPath(filepath.Join(dir, runKnownHostsFile))},
		{"HostKeyAlias", runHostKeyAlias},
		{"StrictHostKeyChecking", "yes"},
	}, nil
}

func sshDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cli: locate ~/.ssh: %w", err)
	}
	return filepath.Join(home, ".ssh"), nil
}

// sshConfigPath writes a path as one ssh_config argument. ssh on Windows
// takes forward slashes, which need no escaping inside the quotes.
func sshConfigPath(path string) string {
	return `"` + filepath.ToSlash(path) + `"`
}

// SSHConfigResult is what InstallSSHConfig wrote.
type SSHConfigResult struct {
	// Hosts is the file holding the Host block; Config is the user's own
	// ssh config, which includes it.
	Hosts, Config string
	// Included reports that this call added the Include line.
	Included bool
}

// InstallSSHConfig writes the Host block for runs to a file of its own and
// makes the user's ssh config include it. The Include goes first because ssh
// keeps the first value it reads for an option, and an include does not end
// or extend the block around it. Nothing else in the user's file changes.
func InstallSSHConfig() (SSHConfigResult, error) {
	options, err := RunSSHOptions()
	if err != nil {
		return SSHConfigResult{}, err
	}
	dir, err := sshDir()
	if err != nil {
		return SSHConfigResult{}, err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return SSHConfigResult{}, fmt.Errorf("cli: create %s: %w", dir, err)
	}
	var hosts strings.Builder
	hosts.WriteString("# Written by `aether ssh-config`. Run it again instead of editing this file.\n")
	hosts.WriteString("Host *" + RunHostSuffix + "\n")
	for _, o := range options {
		hosts.WriteString("  " + o.Key + " " + o.Value + "\n")
	}
	out := SSHConfigResult{Hosts: filepath.Join(dir, runHostsFile), Config: filepath.Join(dir, "config")}
	if err = os.WriteFile(out.Hosts, []byte(hosts.String()), 0o600); err != nil {
		return SSHConfigResult{}, fmt.Errorf("cli: write %s: %w", out.Hosts, err)
	}
	include := "Include " + sshConfigPath(out.Hosts)
	existing, err := os.ReadFile(out.Config)
	if err != nil && !os.IsNotExist(err) {
		return SSHConfigResult{}, fmt.Errorf("cli: read %s: %w", out.Config, err)
	}
	if slices.Contains(strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n"), include) {
		return out, nil
	}
	updated := "# Aether runs as ssh hosts: <run-id>" + RunHostSuffix + " (aether ssh-config)\n" + include + "\n"
	if len(existing) > 0 {
		updated += "\n" + string(existing)
	}
	if err = replaceFile(out.Config, []byte(updated)); err != nil {
		return SSHConfigResult{}, fmt.Errorf("cli: update %s: %w", out.Config, err)
	}
	out.Included = true
	return out, nil
}

// replaceFile swaps in the new content with a rename, so an interrupted
// write cannot truncate the file. It writes through a symlink and keeps the
// mode of a file that exists.
func replaceFile(path string, content []byte) error {
	mode := os.FileMode(0o600)
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
		if info, serr := os.Stat(path); serr == nil {
			mode = info.Mode().Perm()
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".aether-ssh-config-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(content)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// TrustRunHostKey records the host key a run's SSH server presents in the
// known_hosts file RunSSHOptions names. The key arrived over a connection
// whose own host key the CLI already verified, so ssh can check it strictly
// without ever prompting.
func TrustRunHostKey(authorizedKey string) error {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return fmt.Errorf("cli: the server sent an unreadable host key: %w", err)
	}
	dir, err := sshDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, runKnownHostsFile)
	line := []byte(knownhosts.Line([]string{runHostKeyAlias}, key))
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cli: read %s: %w", path, err)
	}
	if slices.ContainsFunc(bytes.Split(existing, []byte("\n")), func(l []byte) bool { return bytes.Equal(l, line) }) {
		return nil
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cli: create %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cli: update %s: %w", path, err)
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("cli: update %s: %w", path, err)
	}
	return nil
}
