package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const memberMergeUsage = "usage: aether member merge start [--keep-admin] | aether member merge <code> [--keep <member-id>] [--keep-admin] [--yes]"

// memberMerge mints a merge code on one sign-in, or previews and confirms
// one minted on the other.
func memberMerge(args []string) error {
	if len(args) > 0 && args[0] == "start" {
		fs := flag.NewFlagSet("member merge start", flag.ContinueOnError)
		keepAdmin := fs.Bool("keep-admin", false, "as an admin, let the merged member stay admin if the other sign-in confirms with --keep-admin")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return errors.New(memberMergeUsage)
		}
		return withControl(func(c *protocol.Client) error {
			var res protocol.MemberMergeStartResult
			if err := c.Call(protocol.MethodMemberMergeStart, protocol.MemberMergeStartParams{KeepAdmin: *keepAdmin}, &res); err != nil {
				return err
			}
			fmt.Printf("merge code %s, valid until %s\n", res.Code, res.ExpiresAt)
			fmt.Printf("on your other sign-in run: aether member merge %s, or open Profile > Sign-ins > Merge in the dashboard\n", res.Code)
			fmt.Println("whoever enters this code merges their member with yours; give it to no one")
			return nil
		})
	}
	fs := flag.NewFlagSet("member merge", flag.ContinueOnError)
	keep := fs.String("keep", "", "the member id whose settings prevail")
	keepAdmin := fs.Bool("keep-admin", false, "keep the merged member admin when one of the two is")
	yes := fs.Bool("yes", false, "discard the other member's settings without asking")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New(memberMergeUsage)
	}
	code := args[0]
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New(memberMergeUsage)
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	if !interactive && (*keep == "" || !*yes) {
		return errors.New("aether member merge: no terminal to ask on; pass --keep <member-id> and --yes after reading the preview")
	}
	return withControl(func(c *protocol.Client) error {
		var plan protocol.MemberMergePlan
		if err := c.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: code}, &plan); err != nil {
			return err
		}
		params, err := mergeDecision(plan, code, *keep, *keepAdmin, *yes, bufio.NewReader(os.Stdin), os.Stdout)
		if err != nil {
			return err
		}
		var res protocol.MemberMergeConfirmResult
		if err := c.Call(protocol.MethodMemberMergeConfirm, params, &res); err != nil {
			return err
		}
		fmt.Printf("merged member %s into %s (%s), now %s\n", res.Merge.AbsorbedID, res.Member.ID, res.Member.DisplayName, res.Member.Role)
		if res.Merge.ArchivePath != "" {
			fmt.Printf("its home was moved to %s on the server\n", res.Merge.ArchivePath)
		}
		fmt.Println("its discarded settings are kept in: aether member merges")
		return nil
	})
}

// mergeDecision prints plan and asks, unless the flags already answer,
// whose settings prevail, whether to discard the other's, and whether to
// keep admin.
func mergeDecision(plan protocol.MemberMergePlan, code, keep string, keepAdmin, yes bool, in *bufio.Reader, out io.Writer) (protocol.MemberMergeConfirmParams, error) {
	params := protocol.MemberMergeConfirmParams{Code: code, PlanDigest: plan.PlanDigest, KeepAdmin: keepAdmin}
	sides := []protocol.MergeMember{plan.Minted, plan.Confirming}
	for i, m := range sides {
		printMergeMember(out, i+1, m)
	}
	_, _ = fmt.Fprintf(out, "\nthe merged member signs in with: %s\n", strings.Join(plan.SignIns, ", "))
	if keep == "" {
		_, _ = fmt.Fprint(out, "whose settings prevail, 1 or 2? ")
		switch line, _ := in.ReadString('\n'); strings.TrimSpace(line) {
		case "1":
			keep = plan.Minted.ID
		case "2":
			keep = plan.Confirming.ID
		default:
			return params, errors.New("aether member merge: answer 1 or 2; nothing was merged")
		}
	}
	params.KeepSettingsOf = keep
	var choice *protocol.MergeChoice
	for i := range plan.Choices {
		if plan.Choices[i].KeepSettingsOf == keep {
			choice = &plan.Choices[i]
		}
	}
	if choice == nil {
		return params, fmt.Errorf("aether member merge: --keep %s is neither %s nor %s", keep, plan.Minted.ID, plan.Confirming.ID)
	}
	_, _ = fmt.Fprintf(out, "\nkeeping %s; member %s is absorbed and these of its settings are discarded:\n", keep, choice.AbsorbedID)
	printDiscarded(out, choice.Discarded)
	if plan.RoleWithKeepAdmin != "" && !keepAdmin {
		_, _ = fmt.Fprintf(out, "\nthe merged member will be %s, the lower of the two roles", plan.Role)
		if plan.KeepAdminRequired {
			_, _ = fmt.Fprint(out, ", which leaves the server without an admin")
		}
		switch {
		case !plan.KeepAdminConsented:
			_, _ = fmt.Fprintln(out, "; to keep it admin, mint the code on the admin sign-in with aether member merge start --keep-admin")
		case yes:
			_, _ = fmt.Fprintln(out, "; pass --keep-admin to keep it admin")
		default:
			_, _ = fmt.Fprintf(out, "\nkeep it %s instead? every sign-in above then reaches the server as admin [y/N]: ", plan.RoleWithKeepAdmin)
			params.KeepAdmin = yesAnswer(in)
		}
	}
	role := plan.Role
	if params.KeepAdmin && plan.RoleWithKeepAdmin != "" {
		role = plan.RoleWithKeepAdmin
	}
	_, _ = fmt.Fprintf(out, "\nthe merged member will be %s\n", role)
	if !yes {
		_, _ = fmt.Fprint(out, "discard these settings and merge? [y/N]: ")
		if !yesAnswer(in) {
			return params, errors.New("aether member merge: not merged")
		}
	}
	params.DiscardAck = true
	return params, nil
}

func yesAnswer(in *bufio.Reader) bool {
	line, _ := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func printMergeMember(out io.Writer, n int, m protocol.MergeMember) {
	_, _ = fmt.Fprintf(out, "%d. %s (%s), %s\n", n, m.DisplayName, m.ID, m.Role)
	if m.TailnetLogin != "" {
		_, _ = fmt.Fprintf(out, "   tailnet login: %s\n", m.TailnetLogin)
	}
	if m.PublicKeyFingerprint != "" {
		_, _ = fmt.Fprintf(out, "   SSH key: %s\n", m.PublicKeyFingerprint)
	}
	for _, id := range m.Identities {
		_, _ = fmt.Fprintf(out, "   %s account: %s\n", id.Provider, firstNonEmpty(id.Login, id.Email, id.Subject))
	}
	for _, d := range m.Devices {
		_, _ = fmt.Fprintf(out, "   device %s %q: %s\n", d.ID, d.Label, d.Status)
	}
	_, _ = fmt.Fprintf(out, "   %d runs, %d schedules\n", m.Runs, m.Schedules)
}

func printDiscarded(out io.Writer, s protocol.MergeSettings) {
	_, _ = fmt.Fprintf(out, "  display name %q, color %s\n", s.DisplayName, s.Color)
	if s.GitName != "" || s.GitEmail != "" {
		_, _ = fmt.Fprintf(out, "  git identity %s <%s>\n", s.GitName, s.GitEmail)
	}
	if s.Image != "" {
		_, _ = fmt.Fprintf(out, "  saved environment image %s, deleted by cache cleanup\n", s.Image)
	}
	for _, d := range s.HarnessDefinitions {
		_, _ = fmt.Fprintf(out, "  custom agent %s\n", d.Name)
	}
	for _, h := range s.ProfileHeads {
		_, _ = fmt.Fprintf(out, "  current %s profile, snapshot %s (kept as the merged member's snapshot)\n", h.Harness, h.SnapshotID)
	}
	if len(s.AccountSharedWith) > 0 {
		_, _ = fmt.Fprintf(out, "  agent account shared with %s\n", strings.Join(s.AccountSharedWith, ", "))
	}
	if len(s.MirrorWorkspaces) > 0 {
		_, _ = fmt.Fprintf(out, "  GitHub connection of the mirrors of workspaces %s: reconnect GitHub on the merged member\n", strings.Join(s.MirrorWorkspaces, ", "))
	}
	if s.Home {
		_, _ = fmt.Fprintln(out, "  home (agent logins, git signing key, files), moved to <data-dir>/merged/<merge id>/home on the server")
	}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// memberMerges lists the merges into your member; an admin sees every one.
func memberMerges() error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberMergeListResult
		if err := c.Call(protocol.MethodMemberMergeList, struct{}{}, &res); err != nil {
			return err
		}
		for _, m := range res.Merges {
			fmt.Printf("%s %s: %s (%s) absorbed into %s, %s+%s -> %s, confirmed by %s\n", m.ID, m.CreatedAt,
				m.AbsorbedID, m.Absorbed.DisplayName, m.SurvivorID, m.SurvivorRoleBefore, m.AbsorbedRoleBefore, m.RoleAfter, m.ConfirmedBy)
			printDiscarded(os.Stdout, m.Discarded)
			if m.ArchivePath != "" {
				fmt.Printf("  home archived at %s\n", m.ArchivePath)
			}
		}
		return nil
	})
}
