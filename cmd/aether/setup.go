package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "setup",
		short: "interactive harness login on the server",
		run:   runSetup,
	})
}

type setupOptions struct {
	harness   string
	workspace string
}

func parseSetup(args []string) (setupOptions, error) {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace name or ID")
	harness, err := parseLeadingArg(fs, args)
	if err != nil || harness == "" {
		return setupOptions{}, fmt.Errorf("usage: aether setup <harness> [--workspace <workspace>]")
	}
	return setupOptions{harness: harness, workspace: *workspace}, nil
}

func runSetup(args []string) error {
	opts, err := parseSetup(args)
	if err != nil {
		return err
	}
	return withResolvedWorkspace(opts.workspace, func(selector protocol.WorkspaceSelector) error {
		cols, rows := termSize()
		stream, err := openWorkspaceShell(protocol.WorkspaceShellRequest{
			Workspace: selector,
			Mode:      protocol.WorkspaceShellModeHarnessLogin,
			Harness:   opts.harness,
			Cols:      cols,
			Rows:      rows,
		})
		if err != nil {
			return err
		}
		defer func() { _ = stream.Close() }()
		return copyRaw(stream)
	})
}

func openWorkspaceShell(req protocol.WorkspaceShellRequest) (io.ReadWriteCloser, error) {
	cfg, err := cli.Load()
	if err != nil {
		return nil, err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return nil, err
	}
	stream, err := conn.WorkspaceShell(req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &connStream{ReadWriteCloser: stream, conn: conn}, nil
}

type connStream struct {
	io.ReadWriteCloser
	conn *cli.Conn
}

func (s *connStream) Close() error {
	streamErr := s.ReadWriteCloser.Close()
	connErr := s.conn.Close()
	if streamErr != nil {
		return streamErr
	}
	return connErr
}
