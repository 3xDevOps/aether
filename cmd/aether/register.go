package main

import (
	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type command struct {
	name, short string
	run         func(args []string) error
}

var commands []command

func register(c command) {
	commands = append(commands, c)
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
	return fn(c)
}
