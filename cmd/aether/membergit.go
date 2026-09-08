package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const memberGitUsage = "usage: aether member git [--name <name>] [--email <email>] [member-id]"

// memberGit implements `aether member git [member-id]`: without flags it
// shows the identity commits for that member are authored as; --name and
// --email set it. Members set their own; anyone else's needs admin.
func memberGit(args []string) error {
	fs := flag.NewFlagSet("member git", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "git author name; empty restores the fallback")
	email := fs.String("email", "", "git author email; empty restores the fallback")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 {
		return errors.New(memberGitUsage)
	}
	setName, setEmail := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			setName = true
		case "email":
			setEmail = true
		}
	})
	target := fs.Arg(0)
	return withControl(func(c *protocol.Client) error {
		m, err := memberGitTarget(c, target)
		if err != nil {
			return err
		}
		if setName || setEmail {
			// Both halves travel on every call, so setting one flag
			// leaves the other where the member left it.
			p := protocol.MemberGitParams{MemberID: m.ID, Name: m.GitName, Email: m.GitEmail}
			if setName {
				p.Name = *name
			}
			if setEmail {
				p.Email = *email
			}
			var res protocol.MemberGitResult
			if err := c.Call(protocol.MethodMemberGit, p, &res); err != nil {
				return err
			}
			m = res.Member
		}
		fmt.Printf("member %s %s\n", m.ID, m.DisplayName)
		for _, line := range memberGitLines(m) {
			fmt.Println(line)
		}
		return nil
	})
}

// memberGitTarget resolves the member to show or change; empty means the
// caller.
func memberGitTarget(c *protocol.Client, idOrName string) (protocol.Member, error) {
	if idOrName != "" {
		return resolveMember(c, idOrName)
	}
	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		return protocol.Member{}, err
	}
	return info.Member, nil
}

// memberGitLines renders the identity, naming the fallback wherever the
// member has set nothing so the value is not mistaken for a choice.
func memberGitLines(m protocol.Member) []string {
	id := (&domain.Member{
		ID:          domain.MemberID(m.ID),
		DisplayName: m.DisplayName,
		GitName:     m.GitName,
		GitEmail:    m.GitEmail,
	}).GitIdentity()
	name, email := id.Name, id.Email
	if m.GitName == "" {
		name += " (fallback: display name)"
	}
	if m.GitEmail == "" {
		email += " (fallback)"
	}
	return []string{"git name   " + name, "git email  " + email}
}
