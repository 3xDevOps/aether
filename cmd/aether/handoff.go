package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "handoff",
		short: "transfer a run to another member",
		run:   runHandoff,
	})
}

func runHandoff(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: aether handoff <run-id> <member>")
	}
	runID, who := args[0], args[1]
	return withControl(func(c *protocol.Client) error {
		to, err := resolveMember(c, who)
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
			RunID:      runID,
			ToMemberID: to.ID,
		}, nil); err != nil {
			return err
		}
		name := to.DisplayName
		if term.IsTerminal(int(os.Stdout.Fd())) {
			name = attribution.Sprint(to.Color, name)
		}
		fmt.Printf("handed off %s to %s (%s)\n", runID, name, to.ID)
		return nil
	})
}

// memberDirectory indexes every member by ID, for surfaces that render
// actor names and colors.
func memberDirectory(c *protocol.Client) (map[string]protocol.Member, error) {
	var ml protocol.MemberListResult
	if err := c.Call(protocol.MethodMemberList, struct{}{}, &ml); err != nil {
		return nil, err
	}
	byID := make(map[string]protocol.Member, len(ml.Members))
	for _, m := range ml.Members {
		byID[m.ID] = m
	}
	return byID, nil
}

// resolveMember maps a member ID or display name to that member. An
// ambiguous display name is an error rather than a guess.
func resolveMember(c *protocol.Client, idOrName string) (protocol.Member, error) {
	byID, err := memberDirectory(c)
	if err != nil {
		return protocol.Member{}, err
	}
	if m, ok := byID[idOrName]; ok {
		return m, nil
	}
	var matches []protocol.Member
	for _, m := range byID {
		if strings.EqualFold(m.DisplayName, idOrName) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return protocol.Member{}, fmt.Errorf("member %q not found", idOrName)
	default:
		return protocol.Member{}, fmt.Errorf("member %q is ambiguous; use the member ID", idOrName)
	}
}
