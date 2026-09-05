package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
)

func init() {
	register(command{
		name:  "forward",
		short: "forward a run container port to localhost",
		run:   runForward,
	})
}

func runForward(args []string) error {
	runID, port, localPort, err := parseForwardArgs(args)
	if err != nil {
		return err
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	manager := localops.NewForwardManager()
	defer manager.Close()
	if err := manager.Start(runID, localPort, func() (io.ReadWriteCloser, error) {
		return conn.Forward(runID, uint32(port))
	}); err != nil {
		return err
	}
	fmt.Printf("forwarding 127.0.0.1:%d -> run %s port %d (Ctrl-C to stop)\n", localPort, runID, port)

	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, terminationSignals...)
	defer signal.Stop(stopped)
	<-stopped
	fmt.Fprintln(os.Stderr, "aether: stopping forward")
	return manager.Stop(runID, localPort)
}

func parseForwardArgs(args []string) (string, int, int, error) {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	local := fs.Int("local", 0, "local loopback port (default: remote port)")
	var positional, flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--local" {
			flags = append(flags, arg)
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			continue
		}
		positional = append(positional, arg)
	}
	if err := fs.Parse(flags); err != nil || len(positional) != 2 {
		return "", 0, 0, fmt.Errorf("usage: aether forward <run-id> <port> [--local <port>]")
	}
	port, err := strconv.Atoi(positional[1])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, 0, fmt.Errorf("port must be between 1 and 65535")
	}
	localPort := *local
	if localPort == 0 {
		localPort = port
	}
	if localPort < 1 || localPort > 65535 {
		return "", 0, 0, fmt.Errorf("local port must be between 1 and 65535")
	}
	return positional[0], port, localPort, nil
}
