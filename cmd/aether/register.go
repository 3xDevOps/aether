package main

import (
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type command struct {
	name, short string
	// usage holds one line per form, each starting "aether <name>", and an
	// indented line continues the one before it; help for a subcommand
	// shows only the forms that start with its words.
	usage   string
	example string
	// flags is set when run parses a flag set named name before it does
	// anything else, so dispatch hands it -h and the help lists each flag.
	flags bool
	run   func(args []string) error
}

var commands []command

func register(c command) {
	commands = append(commands, c)
}

// flagSubcommands lists the subcommand paths whose handler parses a flag
// set named after the path before it reads, writes or dials anything, so
// dispatch runs it with the help flag and the help lists each flag. A
// handler that acts first, or that never parses the help flag, stays out.
var flagSubcommands = []string{
	"template list", "template save", "template delete",
	"budget set",
	"schedule list", "schedule delete",
	"workspace init", "workspace add", "workspace delete", "workspace settings", "workspace origin",
	"workspace env list", "workspace env set", "workspace env unset", "workspace env import", "workspace env script",
	"workspace env file put", "workspace env file rm",
	"workspace mirror status", "workspace mirror configure", "workspace mirror refresh",
	"workspace mirror adopt", "workspace mirror disable",
	"swarm create", "swarm list",
	"profile push", "profile status", "profile rollback",
	"member git", "member assistant", "member link",
	"daemon run", "daemon install",
	"server update", "gui build",
}

var helpFlags *flag.FlagSet

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() { helpFlags = fs }
	return fs
}

func withControl(fn func(*protocol.Client) error) error {
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	err = fn(c)
	if errors.Is(err, errInterrupted) {
		// The abandoned call may be stuck writing to SSH, and closing the
		// control channel writes to the same connection.
		_ = conn.Close()
	}
	// mcp.go wraps its own calls with olderServer already.
	if err != nil && strings.Contains(err.Error(), "aether server update") {
		return err
	}
	return olderServer(err)
}
