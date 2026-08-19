package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "invite",
		short: "mint a one-time invite code",
		run:   runInvite,
	})
}

func runInvite(args []string) error {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	ttl := fs.Int("ttl", 86400, "lifetime in seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberInviteResult
		if err := c.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{TTLSeconds: *ttl}, &res); err != nil {
			return err
		}
		fmt.Println(res.Code)
		fmt.Fprintf(os.Stderr, "expires %s\n", res.ExpiresAt)
		return nil
	})
}
