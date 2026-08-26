package main

import (
	"flag"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "budget",
		short: "show or set a workspace's spend cap",
		run:   runBudget,
	})
}

func runBudget(args []string) error {
	if len(args) > 0 && args[0] == "set" {
		return budgetSet(args[1:])
	}
	fs := flag.NewFlagSet("budget", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.BudgetResult
		if err := c.Call(protocol.MethodBudgetGet, protocol.BudgetGetParams{WorkspaceID: wsID}, &res); err != nil {
			return err
		}
		printBudget(res)
		return nil
	})
}

func budgetSet(args []string) error {
	fs := flag.NewFlagSet("budget set", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	limit := fs.Float64("limit", 0, "hard cap in USD; new runs are refused at it (0 clears the budget, omitted keeps the current cap)")
	warn := fs.Float64("warn", 0, "soft warning threshold in USD (0 for none, omitted keeps the current one)")
	override := fs.Bool("override", false, "admit new runs past the cap until this is turned off again with -override=false")
	if err := fs.Parse(args); err != nil {
		return err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		change := protocol.BudgetSetParams{
			WorkspaceID: wsID,
			LimitUSD:    *limit,
			WarnUSD:     *warn,
			Override:    *override,
		}
		if err := carryBudget(c, &change, given); err != nil {
			return err
		}
		var res protocol.BudgetResult
		if err := c.Call(protocol.MethodBudgetSet, change, &res); err != nil {
			return err
		}
		printBudget(res)
		return nil
	})
}

// carryBudget fills the fields the admin did not name with the budget's
// stored values, so a partial edit edits one field instead of replacing
// the whole budget. An omitted -limit with no budget to carry forward is
// an error: sending zero would clear a budget the admin never mentioned.
func carryBudget(c *protocol.Client, change *protocol.BudgetSetParams, given map[string]bool) error {
	if given["limit"] && given["warn"] && given["override"] {
		return nil
	}
	var cur protocol.BudgetResult
	if err := c.Call(protocol.MethodBudgetGet, protocol.BudgetGetParams{WorkspaceID: change.WorkspaceID}, &cur); err != nil {
		return err
	}
	if cur.Budget == nil {
		if !given["limit"] {
			return fmt.Errorf("this workspace has no budget to edit: pass -limit to set one")
		}
		return nil
	}
	if !given["limit"] {
		change.LimitUSD = cur.Budget.LimitUSD
	}
	if !given["warn"] {
		change.WarnUSD = cur.Budget.WarnUSD
	}
	if !given["override"] {
		change.Override = cur.Budget.Override
	}
	return nil
}

func printBudget(res protocol.BudgetResult) {
	if res.Budget == nil {
		fmt.Printf("no budget set; %s spent so far (%s)\n", usd(res.Spend.CostUSD), meteringNote(res))
		return
	}
	fmt.Printf("cap %s, spent %s (%s)\n", usd(res.Budget.LimitUSD), usd(res.Spend.CostUSD), res.State)
	if res.Budget.WarnUSD > 0 {
		fmt.Printf("warning threshold %s\n", usd(res.Budget.WarnUSD))
	}
	if res.Budget.Override {
		fmt.Println("override ON: new runs start past the cap until an admin turns it off")
	} else if res.State == "exceeded" {
		fmt.Println("new runs are refused; raise the cap or set --override to admit them")
	}
	fmt.Println(meteringNote(res))
}

// meteringNote says whether the cap is a measurement or an estimate.
// Unmetered runs report no usage at all, so a budget covering any of them
// is advisory.
func meteringNote(res protocol.BudgetResult) string {
	if !res.Advisory {
		return fmt.Sprintf("all %d runs metered", res.Spend.Runs)
	}
	return fmt.Sprintf("advisory: %d of %d runs are unmetered, so real spend is higher than the number above",
		res.Spend.Unmetered, res.Spend.Runs)
}
