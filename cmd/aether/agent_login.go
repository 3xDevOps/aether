package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"golang.org/x/term"
)

const loginExitMark = "aether-login-exit"

var loginExitLine = regexp.MustCompile(`^` + loginExitMark + ` ([0-9]+)$`)

func agentLogin(args []string) error {
	if len(args) != 1 || args[0] == "" || args[0][0] == '-' {
		return errors.New("usage: aether agent login <name>")
	}
	var agent protocol.AgentInfo
	found := false
	if err := withControl(func(c *protocol.Client) error {
		var err error
		agent, found, err = lookupAgent(c, args[0])
		return err
	}); err != nil {
		return err
	}
	switch {
	case !found:
		return fmt.Errorf("no agent %q; aether agent list shows yours", args[0])
	case agent.LoginCommand == "":
		return fmt.Errorf("agent %s has no sign-in command Aether knows; run its own login in aether terminal", agent.Name)
	case !agent.Installed:
		return fmt.Errorf("agent %s is not installed in your environment; run aether agent add %s first", agent.Name, agent.Name)
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return typeInTerminal(agent.LoginCommand)
	}
	if agent.LoginKind != "device" {
		return fmt.Errorf("%s signs in with %s, which needs typed input, so it needs a terminal. The human runs this in their own terminal:\n"+
			"  aether agent login %s\n"+
			"An API key stored as a workspace secret works instead: docs/harnesses.md#use-an-api-key", agent.Name, agent.LoginCommand, agent.Name)
	}
	return deviceLogin(os.Stdout, agent)
}

// deviceLogin runs the sign-in in a tab of its own, so a retry follows a
// sign-in still waiting there instead of starting a second one.
func deviceLogin(out io.Writer, agent protocol.AgentInfo) error {
	tab := "login-" + agent.Name
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
	var status protocol.TerminalStatusResult
	err = c.Call(protocol.MethodTerminalStatus, struct{}{}, &status)
	_ = c.Close()
	if err != nil {
		return err
	}
	running := slices.Contains(status.Tabs, tab)

	cols, rows := termSize()
	stream, _, err := conn.TerminalStream(protocol.TerminalRequest{Tab: tab, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	if running {
		_, err = fmt.Fprintf(out, "a sign-in is already running in terminal tab %s; following it\n", tab)
	} else {
		_, err = fmt.Fprintf(out, "running %s in terminal tab %s; open the URL it prints in your browser and enter the code\n", agent.LoginCommand, tab)
	}
	if err != nil {
		return err
	}
	exit, err := followLogin(stream, agent.LoginCommand, !running, out)
	if err = describeTerminalEnd(err); err != nil {
		return err
	}
	switch {
	case exit < 0:
		return fmt.Errorf("the sign-in in terminal tab %s ended before %s did; run aether agent login %s again", tab, agent.LoginCommand, agent.Name)
	case exit != 0:
		return fmt.Errorf("%s exited %d; its output is above. Run aether agent login %s to try again", agent.LoginCommand, exit, agent.Name)
	}

	c, err = conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	after, _, err := lookupAgent(c, agent.Name)
	if err != nil {
		return err
	}
	switch {
	case after.LoginFound == nil:
		_, err = fmt.Fprintf(out, "%s exited 0\n", agent.LoginCommand)
	case !*after.LoginFound:
		return fmt.Errorf("%s exited 0, but %s's login file is not in your home; run aether agent login %s again", agent.LoginCommand, agent.Name, agent.Name)
	default:
		_, err = fmt.Fprintf(out, "%s: login file found; aether account usage checks that the vendor accepts it\n", agent.Name)
	}
	return err
}

// followLogin returns the sign-in's exit status, or -1 when the tab ended
// without reporting one.
func followLogin(stream io.ReadWriter, command string, start bool, out io.Writer) (int, error) {
	if start {
		if _, err := fmt.Fprintf(stream, "%s; echo %s $?; exit\n", command, loginExitMark); err != nil {
			return -1, err
		}
	}
	exit := -1
	text := &plainLines{line: func(line []byte) error {
		if m := loginExitLine.FindSubmatch(line); m != nil {
			exit, _ = strconv.Atoi(string(m[1]))
			return nil
		}
		_, err := fmt.Fprintf(out, "%s\n", line)
		return err
	}}
	_, err := io.Copy(text, stream)
	if flushErr := text.flush(); err == nil {
		err = flushErr
	}
	return exit, err
}

// plainLines passes a terminal's output on as plain lines. Escape sequences
// are dropped, and a carriage return or a move to the first column starts
// the line over, so a spinner leaves only its last frame.
type plainLines struct {
	line  func([]byte) error
	buf   []byte
	state byte
	cr    bool
}

const (
	plainText byte = iota
	plainEscape
	plainCSI
	plainString
	plainStringEscape
	plainIntermediate
)

func (p *plainLines) Write(b []byte) (int, error) {
	for _, c := range b {
		switch p.state {
		case plainEscape:
			switch {
			case c == '[':
				p.state = plainCSI
			case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
				p.state = plainString
			case c >= 0x20 && c <= 0x2f:
				p.state = plainIntermediate
			default:
				p.state = plainText
			}
		case plainCSI:
			if c >= 0x40 && c <= 0x7e {
				p.state = plainText
				if c == 'G' {
					p.buf = p.buf[:0]
				}
			}
		case plainString:
			switch c {
			case 0x07:
				p.state = plainText
			case 0x1b:
				p.state = plainStringEscape
			}
		case plainStringEscape:
			if c == '\\' {
				p.state = plainText
			} else if c != 0x1b {
				p.state = plainString
			}
		case plainIntermediate:
			if c < 0x20 || c > 0x2f {
				p.state = plainText
			}
		default:
			switch {
			case c == 0x1b:
				p.state = plainEscape
			case c == '\n':
				if err := p.emit(); err != nil {
					return 0, err
				}
			case c == '\r':
				p.cr = true
			case c == '\t' || (c >= 0x20 && c != 0x7f):
				if p.cr {
					p.buf, p.cr = p.buf[:0], false
				}
				p.buf = append(p.buf, c)
			}
		}
	}
	return len(b), nil
}

func (p *plainLines) emit() error {
	err := p.line(bytes.TrimRight(p.buf, " \t"))
	p.buf, p.cr = p.buf[:0], false
	return err
}

func (p *plainLines) flush() error {
	if len(bytes.TrimSpace(p.buf)) == 0 {
		return nil
	}
	return p.emit()
}
