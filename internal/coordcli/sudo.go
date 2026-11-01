package coordcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const sudoUsage = "usage: sudo [-u user] [-E] [-H] [-n] [--] command [args...]"

var errSudoUsage = errors.New(sudoUsage)

// Sudo is the sudo of a Default or Isolated run: cmd/aether-server runs it
// when argv0 is sudo. The server decides whether the command runs, and the
// shim exits with the command's status, or 1 when it did not run.
func Sudo(args []string) int {
	var stdin io.Reader = os.Stdin
	if term.IsTerminal(int(os.Stdin.Fd())) {
		stdin = nil
	}
	return runSudo(context.Background(), defaultSocketPath, args, stdin, os.Stdout, os.Stderr)
}

// runSudo forwards a nil stdin as no input.
func runSudo(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	p, err := parseSudo(args)
	if err == nil {
		p.Dir, err = os.Getwd()
	}
	var input []byte
	if err == nil && stdin != nil {
		if input, err = io.ReadAll(io.LimitReader(stdin, protocol.RunSudoMaxStdin+1)); err == nil && len(input) > protocol.RunSudoMaxStdin {
			err = fmt.Errorf("standard input passes %d bytes; write it to a file and pass the file to the command", protocol.RunSudoMaxStdin)
		}
	}
	code := 1
	if err == nil {
		code, err = callSudo(ctx, socket, p, input, stdout, stderr)
	}
	switch {
	case errors.Is(err, errSudoUsage):
		_, _ = fmt.Fprintln(stderr, sudoUsage)
		return 1
	case err != nil:
		_, _ = fmt.Fprintln(stderr, "sudo: "+err.Error())
		return 1
	}
	return code
}

// parseSudo accepts sudo [-u user] [-E] [-H] [-n] [--] command [args...].
// -E and -H change nothing: the command always gets the container's
// environment.
func parseSudo(args []string) (protocol.RunSudoParams, error) {
	var p protocol.RunSudoParams
	for len(args) > 0 && args[0] != "-" && strings.HasPrefix(args[0], "-") {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, _ := strings.Cut(arg, "=")
			return p, fmt.Errorf("%s is not supported in an Aether Default session", name)
		}
		for flags := arg[1:]; flags != ""; {
			flag, size := utf8.DecodeRuneInString(flags)
			flags = flags[size:]
			switch flag {
			case 'E', 'H':
			case 'n':
				p.NonInteractive = true
			case 'u':
				p.User, flags = flags, ""
				if p.User == "" && len(args) > 0 {
					p.User, args = args[0], args[1:]
				}
				if p.User == "" {
					return p, errSudoUsage
				}
			default:
				return p, fmt.Errorf("-%c is not supported in an Aether Default session", flag)
			}
		}
	}
	if len(args) == 0 {
		return p, errSudoUsage
	}
	p.Argv = args
	return p, nil
}

// callSudo sends the request line with input right behind it, then copies
// run.sudo.output notifications until the response.
func callSudo(ctx context.Context, socket string, p protocol.RunSudoParams, input []byte, stdout, stderr io.Writer) (int, error) {
	p.StdinBytes = len(input)
	params, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	line, err := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: protocol.MethodRunSudo, Params: params})
	if err != nil {
		return 0, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return 0, fmt.Errorf("coordination is not reachable: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.Write(append(append(line, '\n'), input...)); err != nil {
		return 0, fmt.Errorf("coordination is not reachable: %w", err)
	}
	r := bufio.NewReader(conn)
	for {
		var raw []byte
		if raw, err = protocol.ReadLine(r); err != nil {
			return 0, fmt.Errorf("coordination is not reachable: %w", err)
		}
		var frame struct {
			protocol.Response
			Method string                 `json:"method"`
			Params protocol.RunSudoOutput `json:"params"`
		}
		if err = json.Unmarshal(raw, &frame); err != nil {
			return 0, fmt.Errorf("decode the server's answer: %w", err)
		}
		switch {
		case frame.Method == protocol.MethodRunSudoOutput:
			out := stdout
			if frame.Params.Stderr {
				out = stderr
			}
			if _, err = out.Write(frame.Params.Data); err != nil {
				return 0, err
			}
		case frame.Error != nil:
			return 0, errors.New(frame.Error.Message)
		default:
			var result protocol.RunSudoResult
			if err = json.Unmarshal(frame.Result, &result); err != nil {
				return 0, fmt.Errorf("decode the server's answer: %w", err)
			}
			return result.ExitCode, nil
		}
	}
}
