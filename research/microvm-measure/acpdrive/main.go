// Command acpdrive opens an Enhanced session as the scheduler does (the
// harness profile's ACP command and environment, an acphost session with
// auto-allow, the profile's ACP mode) on an agent inside a VM, over ssh, or a
// container, over docker exec. It plays mock script "turn", then w1, w2, ...
// one per SIGUSR1, and prints when each turn ends. Runs on the KVM host in
// place of the server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

func quote(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellquote.QuoteAlways(a)
	}
	return strings.Join(q, " ")
}

func main() {
	agent := flag.String("agent", "claude", "harness profile")
	target := flag.String("target", "", "vm:<address> or ctr:<container>")
	key := flag.String("key", "/srv/aether/1/keys/a1", "ssh key for vm targets")
	url := flag.String("url", "http://10.201.0.1:18080", "mock model base URL")
	logPath := flag.String("log", "", "acphost item log path")
	flag.Parse()
	log.SetFlags(0)

	p, ok := harness.Lookup(*agent)
	if !ok || len(p.ACPArgs) == 0 {
		log.Fatalf("no Enhanced profile for %q", *agent)
	}
	command := p.ACPCommand(p.ACPArgs, p.TUIArgs[0])
	env := []string{"ANTHROPIC_API_KEY=aether-mock-model-api-key", "ANTHROPIC_BASE_URL=" + *url,
		"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)

	var c *exec.Cmd
	kind, addr, _ := strings.Cut(*target, ":")
	switch kind {
	case "vm":
		c = exec.Command("ssh", "-T", "-q", "-i", *key, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
			"-o", "BatchMode=yes", "root@"+addr, "cd /workspace && exec env "+quote(env)+" "+quote(command))
	case "ctr":
		args := []string{"exec", "-i", "-w", "/workspace"}
		for _, e := range env {
			args = append(args, "-e", e)
		}
		c = exec.Command("docker", append(append(args, addr), command...)...)
	default:
		log.Fatalf("target %q is neither vm: nor ctr:", *target)
	}
	stdin, err := c.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		log.Fatal(err)
	}

	states := make(chan bool, 256)
	ctx := context.Background()
	s, err := acphost.Start(ctx, stdout, stdin, acphost.Config{
		LogPath: *logPath, Cwd: "/workspace", AutoAllow: true, ClaudeCode: *agent == "claude",
		OnState: func(ts acphost.TurnState) {
			select {
			case states <- ts.Working:
			default:
			}
		},
	})
	if err != nil {
		log.Fatalf("acphost start: %v", err)
	}
	if p.ACPMode != "" {
		if err := s.SetMode(ctx, p.ACPMode); err != nil && !acphost.Refused(err) {
			log.Fatalf("set mode %s: %v", p.ACPMode, err)
		}
	}
	fmt.Printf("session %s open (%s)\n", s.SessionID(), quote(command))

	next := make(chan os.Signal, 1)
	signal.Notify(next, syscall.SIGUSR1)
	for n := 0; ; n++ {
		script := "turn"
		if n > 0 {
			script = fmt.Sprintf("w%d", n)
		}
		start := time.Now()
		if _, err := s.Prompt(ctx, []acp.ContentBlock{acp.TextBlock("aether-script:" + script + "; run the command, then stop.")}, false, nil); err != nil {
			log.Fatalf("prompt %s: %v", script, err)
		}
		for working := false; ; {
			select {
			case w := <-states:
				if w {
					working = true
					continue
				}
				if !working {
					continue
				}
			case <-s.Done():
				log.Fatal("agent connection ended")
			}
			break
		}
		fmt.Printf("turn %s done in %s: %q\n", script, time.Since(start).Round(time.Millisecond), acphost.LastMessage(s.Log()))
		select {
		case <-next:
		case <-s.Done():
			log.Fatal("agent connection ended")
		}
	}
}
