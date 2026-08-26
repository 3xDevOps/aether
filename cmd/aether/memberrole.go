package main

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// memberRole implements `aether member role <member-id> <role>`: promote
// or demote a member. Admins only; the server refuses to demote the last
// admin.
func memberRole(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: aether member role <member-id> <viewer|collaborator|admin>")
	}
	memberID, role := args[0], args[1]
	if !domain.Role(role).Valid() {
		return fmt.Errorf("invalid role %q: want viewer, collaborator, or admin", role)
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberRoleResult
		err := c.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
			MemberID: memberID,
			Role:     role,
		}, &res)
		if err != nil {
			return err
		}
		name := res.Member.DisplayName
		if term.IsTerminal(int(os.Stdout.Fd())) {
			name = attribution.Sprint(res.Member.Color, name)
		}
		// "set ... to" reads correctly whether this was a promotion or a
		// demotion, and stays true when the role did not actually change.
		fmt.Printf("set %s %s to %s\n", res.Member.ID, name, res.Member.Role)
		return nil
	})
}
