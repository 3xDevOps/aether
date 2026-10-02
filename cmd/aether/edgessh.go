package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
)

func init() {
	register(command{
		name:  "edge-ssh",
		short: "ssh for git: reaches edge-linked servers, runs ssh for every other host",
		run: func(args []string) error {
			os.Exit(edgeSSH(args, os.Stdin, os.Stdout, os.Stderr))
			return nil
		},
	})
}

// systemSSH runs the system ssh with args unchanged and returns its exit
// status. Tests replace it.
var systemSSH = func(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := exec.Command("ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	default:
		_, _ = fmt.Fprintln(stderr, "aether edge-ssh:", err)
		return 255
	}
}

// edgeTarget returns the parsed command line and the server id when args
// address an edge link's logical host. A command line that does not parse
// is not one: ssh judges it.
func edgeTarget(args []string) (cli.SSHArgs, string, bool) {
	parsed, err := cli.ParseSSHArgs(args)
	if err != nil {
		return cli.SSHArgs{}, "", false
	}
	id, ok := cli.ServerIDFromEdgeHost(parsed.Host)
	return parsed, id, ok
}

// edgeSSH is `aether edge-ssh`, which git runs as ssh: [options]
// [user@]host command. An edge link's logical host runs the command over
// the linked dialer; every other host goes to the system ssh with the
// arguments unchanged. It returns the exit status.
func edgeSSH(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	parsed, serverID, ok := edgeTarget(args)
	if !ok {
		return systemSSH(args, stdin, stdout, stderr)
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(stderr, "aether edge-ssh:", err)
		return 255
	}
	// Git probes with -G and, on failure, passes no options at all.
	if len(parsed.Options) != 0 {
		return fail(fmt.Errorf("ssh options are not supported for edge host %s: %s",
			parsed.Host, strings.Join(parsed.Options, " ")))
	}
	if parsed.Command == "" {
		return fail(fmt.Errorf("edge host %s runs git commands only; no command was given", parsed.Host))
	}
	cfg, err := cli.Load()
	if err != nil {
		return fail(err)
	}
	link, ok := cfg.ByServerID(serverID)
	if !ok {
		return fail(fmt.Errorf("no link has server id %s; link it with: aether link %s", serverID, serverID))
	}
	code, err := cli.RunRemote(context.Background(), link, parsed.User, parsed.Command, stdin, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "aether edge-ssh:", err)
	}
	return code
}
