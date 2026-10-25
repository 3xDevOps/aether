package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const memberMergeUsage = "usage: aether member merge start | aether member merge <code> [--keep <member-id>] [--keep-admin] [--yes] |\n" +
	"       aether member merge accept [<proposal-id>] [--keep-admin] [--yes] | aether member merge cancel"

// memberMerge mints a merge code on one sign-in, proposes a merge with it
// from the other, and accepts or cancels it.
func memberMerge(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New(memberMergeUsage)
	}
	fs := newFlagSet("member merge")
	keep := fs.String("keep", "", "the member id whose settings prevail")
	keepAdmin := fs.Bool("keep-admin", false, "as the admin side, keep the merged member admin")
	yes := fs.Bool("yes", false, "answer yes to the plan printed instead of asking")
	rest, proposalID := args[1:], ""
	if args[0] == "accept" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		proposalID, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return fmt.Errorf("%w\n%s", err, memberMergeUsage)
	}
	if fs.NArg() != 0 {
		return errors.New(memberMergeUsage)
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	in, out := bufio.NewReader(os.Stdin), os.Stdout
	switch args[0] {
	case "start":
		return withControl(func(c *protocol.Client) error {
			var res protocol.MemberMergeStartResult
			if err := c.Call(protocol.MethodMemberMergeStart, struct{}{}, &res); err != nil {
				return err
			}
			fmt.Printf("merge code %s, valid until %s\n", res.Code, res.ExpiresAt)
			fmt.Printf("on your other sign-in run: aether member merge %s, or open Profile > Sign-ins > Merge in the dashboard\n", res.Code)
			fmt.Println("Anyone who enters this code can propose joining their sign-ins to your member; you will be asked to accept.")
			fmt.Println("then accept here: aether member merge accept")
			return nil
		})
	case "cancel":
		return withControl(func(c *protocol.Client) error {
			var res protocol.MemberMergeCancelResult
			if err := c.Call(protocol.MethodMemberMergeCancel, protocol.MemberMergeCancelParams{}, &res); err != nil {
				return err
			}
			fmt.Printf("cancelled %d open merge code(s) and proposal(s)\n", res.Cancelled)
			return nil
		})
	case "accept":
		return withControl(func(c *protocol.Client) error {
			return mergeAccept(c, proposalID, *keepAdmin, *yes, interactive, in, out)
		})
	}
	code := args[0]
	return withControl(func(c *protocol.Client) error {
		var plan protocol.MemberMergePlan
		if err := c.Call(protocol.MethodMemberMergePreview, protocol.MemberMergePreviewParams{Code: code}, &plan); err != nil {
			return err
		}
		params, err := mergeProposal(plan, code, *keep, *keepAdmin, *yes, interactive, in, out)
		if err != nil {
			return err
		}
		var res protocol.MemberMergeProposal
		if err := c.Call(protocol.MethodMemberMergePropose, params, &res); err != nil {
			return err
		}
		fmt.Printf("proposed merge %s, open until %s\n", res.ID, res.ExpiresAt)
		fmt.Printf("on the sign-in that minted the code, member %s, run: aether member merge accept\n", res.MintedID)
		return nil
	})
}

// mergeProposal prints plan and asks, unless the flags already answer,
// whose settings prevail, whether the admin side keeps admin, and whether
// to discard the other member's settings.
func mergeProposal(plan protocol.MemberMergePlan, code, keep string, keepAdmin, yes, interactive bool, in *bufio.Reader, out io.Writer) (protocol.MemberMergeProposeParams, error) {
	params := protocol.MemberMergeProposeParams{Code: code, PlanDigest: plan.PlanDigest, KeepAdmin: keepAdmin}
	printMergePlan(out, plan)
	if !interactive && (keep == "" || !yes) {
		return params, errors.New("aether member merge: no terminal to ask on; pass --keep <member-id> and --yes after reading the plan above")
	}
	if keep == "" {
		_, _ = fmt.Fprint(out, "whose settings prevail, 1 or 2? ")
		switch line, _ := in.ReadString('\n'); strings.TrimSpace(line) {
		case "1":
			keep = plan.Minted.ID
		case "2":
			keep = plan.Proposer.ID
		default:
			return params, errors.New("aether member merge: answer 1 or 2; nothing was proposed")
		}
	}
	params.KeepSettingsOf = keep
	if err := printMergeChoice(out, plan, keep); err != nil {
		return params, err
	}
	if plan.RoleWithKeepAdmin != "" && plan.Proposer.Role == "admin" && !keepAdmin && !yes {
		params.KeepAdmin = askKeepAdmin(out, in, plan)
	}
	if !yes {
		_, _ = fmt.Fprint(out, "discard these settings and merge? [y/N]: ")
		if !yesAnswer(in) {
			return params, errors.New("aether member merge: nothing was proposed")
		}
	}
	params.DiscardAck = true
	return params, nil
}

// mergeAccept shows the proposal made with a code this sign-in minted and
// accepts it on a yes, bound to the plan it printed.
func mergeAccept(c *protocol.Client, proposalID string, keepAdmin, yes, interactive bool, in *bufio.Reader, out io.Writer) error {
	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		return err
	}
	var pending protocol.MemberMergePendingResult
	if err := c.Call(protocol.MethodMemberMergePending, struct{}{}, &pending); err != nil {
		return err
	}
	proposal, err := pickProposal(pending.Proposals, info.Member.ID, proposalID)
	if err != nil {
		return err
	}
	params, err := acceptDecision(*proposal, keepAdmin, yes, interactive, in, out)
	if err != nil {
		return err
	}
	var res protocol.MemberMergeAcceptResult
	if err := c.Call(protocol.MethodMemberMergeAccept, params, &res); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "merged member %s into %s (%s), now %s\n", res.Merge.AbsorbedID, res.Member.ID, res.Member.DisplayName, res.Member.Role)
	if res.Merge.ArchivePath != "" {
		_, _ = fmt.Fprintf(out, "its home was moved to %s on the server\n", res.Merge.ArchivePath)
	}
	_, _ = fmt.Fprintln(out, "its discarded settings are kept in: aether member merges")
	return nil
}

// pickProposal returns the open proposal id made with a code member
// minted, or the only one when id is empty: with several, the person names
// the one they mean.
func pickProposal(proposals []protocol.MemberMergeProposal, member, id string) (*protocol.MemberMergeProposal, error) {
	var mine []*protocol.MemberMergeProposal
	for i := range proposals {
		if proposals[i].MintedID == member && (id == "" || proposals[i].ID == id) {
			mine = append(mine, &proposals[i])
		}
	}
	switch {
	case len(mine) == 0 && id != "":
		return nil, fmt.Errorf("aether member merge accept: no open proposal %s was made with a code from this sign-in", id)
	case len(mine) == 0:
		return nil, errors.New("aether member merge accept: no merge was proposed with a code from this sign-in; enter the code on your other sign-in first")
	case len(mine) > 1:
		ids := make([]string, len(mine))
		for i, p := range mine {
			ids[i] = p.ID + " from member " + p.ProposerID
		}
		return nil, fmt.Errorf("aether member merge accept: %d proposals are open (%s); name one: aether member merge accept <proposal-id>",
			len(mine), strings.Join(ids, ", "))
	}
	if mine[0].Refusal != "" || mine[0].Plan == nil {
		return nil, fmt.Errorf("aether member merge accept: %s", mine[0].Refusal)
	}
	return mine[0], nil
}

func acceptDecision(p protocol.MemberMergeProposal, keepAdmin, yes, interactive bool, in *bufio.Reader, out io.Writer) (protocol.MemberMergeAcceptParams, error) {
	plan := *p.Plan
	params := protocol.MemberMergeAcceptParams{ProposalID: p.ID, PlanDigest: plan.PlanDigest, KeepAdmin: keepAdmin}
	_, _ = fmt.Fprintf(out, "member %s proposes merging with your member %s (proposal %s)\n", p.ProposerID, p.MintedID, p.ID)
	printMergePlan(out, plan)
	if err := printMergeChoice(out, plan, p.KeepSettingsOf); err != nil {
		return params, err
	}
	if !interactive && !yes {
		return params, fmt.Errorf("aether member merge accept: no terminal to ask on; pass --yes to accept the plan above: aether member merge accept %s --yes", p.ID)
	}
	if plan.RoleWithKeepAdmin != "" && plan.Minted.Role == "admin" && !keepAdmin && !yes && !p.ProposerKeepAdmin {
		params.KeepAdmin = askKeepAdmin(out, in, plan)
	}
	role := plan.Role
	if plan.RoleWithKeepAdmin != "" && (params.KeepAdmin || (p.ProposerKeepAdmin && plan.Proposer.Role == "admin")) {
		role = plan.RoleWithKeepAdmin
	}
	_, _ = fmt.Fprintf(out, "\nthe merged member will be %s\n", role)
	if !yes {
		_, _ = fmt.Fprint(out, "accept this merge? [y/N]: ")
		if !yesAnswer(in) {
			return params, errors.New("aether member merge accept: not merged; aether member merge cancel drops the proposal")
		}
	}
	return params, nil
}

func askKeepAdmin(out io.Writer, in *bufio.Reader, plan protocol.MemberMergePlan) bool {
	_, _ = fmt.Fprintf(out, "\nthe merged member will be %s, the lower of the two roles", plan.Role)
	if plan.KeepAdminRequired {
		_, _ = fmt.Fprint(out, ", which leaves the server without an admin")
	}
	_, _ = fmt.Fprintf(out, ". keep it %s? every sign-in above then reaches the server as admin [y/N]: ", plan.RoleWithKeepAdmin)
	return yesAnswer(in)
}

func yesAnswer(in *bufio.Reader) bool {
	line, _ := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func printMergePlan(out io.Writer, plan protocol.MemberMergePlan) {
	for i, m := range []protocol.MergeMember{plan.Minted, plan.Proposer} {
		_, _ = fmt.Fprintf(out, "%d. %s (%s), %s\n", i+1, m.DisplayName, m.ID, m.Role)
		if m.TailnetLogin != "" {
			_, _ = fmt.Fprintf(out, "   tailnet login: %s\n", m.TailnetLogin)
		}
		if m.PublicKeyFingerprint != "" {
			_, _ = fmt.Fprintf(out, "   SSH key: %s\n", m.PublicKeyFingerprint)
		}
		for _, id := range m.Identities {
			_, _ = fmt.Fprintf(out, "   %s account: %s\n", id.Provider, id.Login)
		}
		for _, d := range m.Devices {
			_, _ = fmt.Fprintf(out, "   device %q: %s\n", d.Label, d.Status)
		}
		_, _ = fmt.Fprintf(out, "   %d runs, %d schedules\n", m.Runs, m.Schedules)
	}
	_, _ = fmt.Fprintf(out, "\nthe merged member signs in with: %s, as %s\n", strings.Join(plan.SignIns, ", "), plan.Role)
	if plan.RoleWithKeepAdmin != "" {
		_, _ = fmt.Fprintf(out, "or as %s if the admin side asks for it with --keep-admin\n", plan.RoleWithKeepAdmin)
	}
}

func printMergeChoice(out io.Writer, plan protocol.MemberMergePlan, keep string) error {
	for _, c := range plan.Choices {
		if c.KeepSettingsOf == keep {
			_, _ = fmt.Fprintf(out, "\nkeeping %s; member %s is absorbed and these of its settings are discarded:\n", keep, c.AbsorbedID)
			printDiscarded(out, c.Discarded)
			return nil
		}
	}
	return fmt.Errorf("aether member merge: --keep %s is neither %s nor %s", keep, plan.Minted.ID, plan.Proposer.ID)
}

func printDiscarded(out io.Writer, s protocol.MergeSettings) {
	_, _ = fmt.Fprintf(out, "  display name %q, color %s\n", s.DisplayName, s.Color)
	if s.GitName != "" || s.GitEmail != "" {
		_, _ = fmt.Fprintf(out, "  git identity %s %s\n", s.GitName, s.GitEmail)
	}
	if s.Image != "" {
		_, _ = fmt.Fprintf(out, "  saved environment image %s, deleted by cache cleanup\n", s.Image)
	}
	for _, d := range s.HarnessDefinitions {
		_, _ = fmt.Fprintf(out, "  custom agent %s\n", d.Name)
	}
	for _, h := range s.ProfileHeads {
		_, _ = fmt.Fprintf(out, "  current %s profile, snapshot %s\n", h.Harness, h.SnapshotID)
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

// memberMerges lists the merges into your member; an admin sees every one.
func memberMerges() error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberMergeListResult
		if err := c.Call(protocol.MethodMemberMergeList, struct{}{}, &res); err != nil {
			return err
		}
		for _, m := range res.Merges {
			fmt.Printf("%s %s: %s (%s) absorbed into %s, %s+%s -> %s, accepted on %s\n", m.ID, m.CreatedAt,
				m.AbsorbedID, m.Absorbed.DisplayName, m.SurvivorID, m.SurvivorRoleBefore, m.AbsorbedRoleBefore, m.RoleAfter, m.ConfirmedBy)
			printDiscarded(os.Stdout, m.Discarded)
			for _, d := range m.Discarded.HarnessDefinitions {
				fmt.Printf("  custom agent %s definition: %s\n", d.Name, d.Definition)
			}
			if m.ArchivePath != "" {
				fmt.Printf("  home archived at %s\n", m.ArchivePath)
			}
		}
		return nil
	})
}
