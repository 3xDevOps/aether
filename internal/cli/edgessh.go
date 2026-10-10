package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/shellquote"
)

// SSHArgs is an ssh command line as git passes it:
// [options] [user@]host [command].
type SSHArgs struct {
	// Options are the option words before the destination, values
	// included, as given.
	Options []string
	User    string
	Host    string
	Command string
}

// OpenSSH's option letters: those that stand alone and those that take a
// value, either attached (-p22) or as the next word (-p 22).
const (
	sshFlags       = "46AaCfGgKkMNnqsTtVvXxYy"
	sshValueLetter = "BbcDEeFIiJLlmOoPpQRSWw"
)

// ParseSSHArgs splits an ssh command line the way OpenSSH does. The
// command is the remaining words joined by spaces, which is what ssh
// sends to the server.
func ParseSSHArgs(args []string) (SSHArgs, error) {
	var out SSHArgs
	i := 0
	for ; i < len(args); i++ {
		word := args[i]
		if word == "--" {
			i++
			break
		}
		if len(word) < 2 || word[0] != '-' {
			break
		}
		out.Options = append(out.Options, word)
	letters:
		for j := 1; j < len(word); j++ {
			switch c := word[j]; {
			case strings.IndexByte(sshFlags, c) >= 0:
			case strings.IndexByte(sshValueLetter, c) >= 0:
				if j == len(word)-1 {
					if i+1 == len(args) {
						return SSHArgs{}, fmt.Errorf("ssh option -%c needs a value", c)
					}
					i++
					out.Options = append(out.Options, args[i])
				}
				break letters
			default:
				return SSHArgs{}, fmt.Errorf("unknown ssh option -%c", c)
			}
		}
	}
	if i == len(args) {
		return SSHArgs{}, errors.New("ssh command line has no destination")
	}
	out.Host = args[i]
	if at := strings.LastIndexByte(out.Host, '@'); at >= 0 {
		out.User, out.Host = out.Host[:at], out.Host[at+1:]
	}
	out.Command = strings.Join(args[i+1:], " ")
	return out, nil
}

// EdgeSSHCommand is the ssh command git runs to reach edge links: this
// binary's edge-ssh, quoted for the shell git runs it with.
func EdgeSSHCommand() (string, error) {
	exe, err := selfPath()
	if err != nil {
		return "", err
	}
	return shellquote.Quote(exe) + " edge-ssh", nil
}

// selfPath is this binary as a saved command should name it: by the PATH
// entry that leads to it when there is one. On Linux os.Executable resolves
// symlinks, often to a versioned directory an upgrade deletes, and a git or
// ssh configuration keeps the command for good.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cli: resolve aether binary path: %w", err)
	}
	if onPath, err := exec.LookPath("aether"); err == nil && sameFile(onPath, exe) {
		exe = onPath
	}
	return exe, nil
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// GitSSHEnv is what a git process whose remote is rawURL adds to its
// environment: GIT_SSH_COMMAND naming EdgeSSHCommand when the URL's host
// is an edge link's logical host, and nothing otherwise, so every other
// remote keeps the user's own ssh setup.
func GitSSHEnv(rawURL string) ([]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "ssh" {
		return nil, nil
	}
	if _, ok := ServerIDFromEdgeHost(u.Hostname()); !ok {
		return nil, nil
	}
	command, err := EdgeSSHCommand()
	if err != nil {
		return nil, err
	}
	return []string{"GIT_SSH_COMMAND=" + command}, nil
}

// RunRemote runs command on the server of cfg, a link with a server id,
// the way ssh runs a remote command for git: stdin, stdout and stderr are
// wired through and the remote exit status is returned. A failure to run
// the command at all returns 255, as ssh does. An empty user is the
// link's.
func RunRemote(ctx context.Context, cfg Config, user, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if user == "" {
		user = cfg.user()
	}
	client, err := DialLinked(ctx, cfg, user)
	if err != nil {
		return 255, err
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return 255, fmt.Errorf("cli: open session on server %s: %w", cfg.ServerID, err)
	}
	defer func() { _ = sess.Close() }()
	in, err := sess.StdinPipe()
	if err != nil {
		return 255, fmt.Errorf("cli: stdin pipe: %w", err)
	}
	sess.Stdout, sess.Stderr = stdout, stderr
	if err = sess.Start(command); err != nil {
		return 255, fmt.Errorf("cli: run %q on server %s: %w", command, cfg.ServerID, err)
	}
	// Session.Wait would also wait for stdin to reach EOF, which git may
	// only send after the command exits; ssh does not wait for it either.
	go func() {
		_, _ = io.Copy(in, stdin)
		_ = in.Close()
	}()
	err = sess.Wait()
	var exit *ssh.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit) && exit.Signal() == "":
		return exit.ExitStatus(), nil
	default:
		return 255, fmt.Errorf("cli: %q on server %s: %w", command, cfg.ServerID, err)
	}
}
