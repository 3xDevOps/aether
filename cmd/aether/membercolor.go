package main

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// memberColor implements `aether member color <hex> [member-id]`: set
// your own attribution color, or (admins only) another member's.
func memberColor(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: aether member color <#rrggbb> [member-id]")
	}
	color := args[0]
	memberID := ""
	if len(args) == 2 {
		memberID = args[1]
	}
	if _, err := attribution.Normalize(color); err != nil {
		return fmt.Errorf("invalid color %q: want #rrggbb", color)
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberColorResult
		err := c.Call(protocol.MethodMemberColor, protocol.MemberColorParams{
			MemberID: memberID,
			Color:    color,
		}, &res)
		if err != nil {
			return err
		}
		name := res.Member.DisplayName
		if term.IsTerminal(int(os.Stdout.Fd())) {
			name = attribution.Sprint(res.Member.Color, name)
		}
		fmt.Printf("colored %s %s %s\n", res.Member.ID, name, res.Member.Color)
		return nil
	})
}
