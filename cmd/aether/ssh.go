package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
)

func init() {
	register(command{
		name:  "ssh",
		short: "open a shell or run a command in a run, through the system ssh",
		run:   runSSH,
	})
	register(command{
		name:  "ssh-config",
		short: "let ssh, scp and editors reach every run as <run-id>" + cli.RunHostSuffix,
		run:   runSSHConfig,
	})
}

const sshUsage = "usage: aether ssh [ssh options] <run> [command]"

// runSSH runs the system ssh against a run with the options aether
// ssh-config would have written, so it needs no ~/.ssh/config. With --stdio
// it is the ProxyCommand those options name.
func runSSH(args []string) error {
	if len(args) == 2 && args[0] == "--stdio" {
		return sshStdio(args[1])
	}
	argv, err := sshArgv(args)
	if err != nil {
		return err
	}
	if _, err = exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("aether ssh runs the system ssh client: %w", err)
	}
	// Ctrl-C belongs to ssh, which shares this terminal: without a handler
	// here the default action would end this process before ssh reported
	// the remote command's status.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	code := systemSSH(argv, os.Stdin, os.Stdout, os.Stderr)
	signal.Stop(interrupts)
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// sshArgv is the system ssh command line for `aether ssh` arguments: the run
// options first, so the caller's own -o for the same keyword does not
// replace them, then the caller's options, the run's host and the command.
func sshArgv(args []string) ([]string, error) {
	parsed, err := cli.ParseSSHArgs(args)
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, sshUsage)
	}
	options, err := cli.RunSSHOptions()
	if err != nil {
		return nil, err
	}
	var argv []string
	for _, o := range options {
		argv = append(argv, "-o", o.Key+"="+o.Value)
	}
	argv = append(argv, parsed.Options...)
	argv = append(argv, strings.TrimSuffix(parsed.Host, cli.RunHostSuffix)+cli.RunHostSuffix)
	if parsed.Command != "" {
		argv = append(argv, parsed.Command)
	}
	return argv, nil
}

// sshStdio carries one SSH connection between ssh, on stdin and stdout, and
// the run's SSH server. Whatever it prints goes to stderr, which ssh passes
// through, so a refusal reaches the person with the server's own reason.
func sshStdio(host string) error {
	run := strings.TrimSuffix(host, cli.RunHostSuffix)
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stream, hostKey, err := conn.RunSSH(run)
	if err != nil {
		return fmt.Errorf("ssh into %s: %w", run, err)
	}
	defer func() { _ = stream.Close() }()
	if err = cli.TrustRunHostKey(hostKey); err != nil {
		return err
	}
	return copyRawStreams(stream, os.Stdin, os.Stdout, 0)
}

func runSSHConfig(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: aether ssh-config")
	}
	result, err := cli.InstallSSHConfig()
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", result.Hosts)
	if result.Included {
		fmt.Printf("added an Include line for it at the top of %s\n", result.Config)
	} else {
		fmt.Printf("%s already includes it\n", result.Config)
	}
	fmt.Printf("ssh <run-id>%s now opens a shell in that run. scp, sftp, rsync and editors take the same host name.\n", cli.RunHostSuffix)
	return nil
}
