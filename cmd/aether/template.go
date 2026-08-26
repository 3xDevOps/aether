package main

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "template",
		short: "list, save, or delete a workspace's task templates",
		run:   runTemplate,
	})
}

// kvFlag collects repeated "--param name=value" pairs.
type kvFlag map[string]string

func (f kvFlag) String() string { return "" }

func (f kvFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return fmt.Errorf("want name=value, got %q", v)
	}
	f[name] = value
	return nil
}

func runTemplate(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "save":
			return templateSave(args[1:])
		case "delete":
			return templateDelete(args[1:])
		case "list":
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("template list", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.TemplateListResult
		if err := c.Call(protocol.MethodTemplateList, protocol.TemplateListParams{WorkspaceID: wsID}, &res); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tAGENT\tMODE\tPARAMS\tBUDGET\tTASK")
		for _, t := range res.Templates {
			budget := "-"
			if t.BudgetUSD > 0 {
				budget = fmt.Sprintf("$%.2f", t.BudgetUSD)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				t.Name, t.Harness, t.Mode, formatParams(t.Params), budget, firstLine(t.Task))
		}
		return tw.Flush()
	})
}

func templateSave(args []string) error {
	fs := flag.NewFlagSet("template save", flag.ExitOnError)
	agent := fs.String("agent", "", "harness name")
	task := fs.String("task", "", "task prompt; {{name}} marks a parameter")
	mode := fs.String("mode", "headless", "tui or headless")
	budget := fs.Float64("budget", 0, "advisory cost hint in USD per run")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	params := kvFlag{}
	fs.Var(params, "param", "default for a task parameter, name=value (repeatable)")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" || *task == "" || *agent == "" {
		return fmt.Errorf("usage: aether template save <name> --agent <name> --task \"...\" [--mode] [--param k=v] [--budget] [--workspace]")
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.TemplateSaveResult
		if err := c.Call(protocol.MethodTemplateSave, protocol.TemplateSaveParams{
			WorkspaceID: wsID,
			Name:        name,
			Task:        *task,
			Harness:     *agent,
			Mode:        *mode,
			Params:      params,
			BudgetUSD:   *budget,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("template %s (%s, %s)\n", res.Template.Name, res.Template.Harness, res.Template.Mode)
		return nil
	})
}

func templateDelete(args []string) error {
	fs := flag.NewFlagSet("template delete", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" {
		return fmt.Errorf("usage: aether template delete <name> [--workspace]")
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodTemplateDelete, protocol.TemplateDeleteParams{
			WorkspaceID: wsID, Name: name,
		}, nil); err != nil {
			return err
		}
		fmt.Printf("deleted template %s\n", name)
		return nil
	})
}

// launchTemplate backs `aether run --template`. It reports the base
// branch's age with the run: a template launch is often unattended work
// on a base the server has not heard about since somebody last pushed.
func launchTemplate(workspace, name string, params kvFlag) error {
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, workspace)
		if err != nil {
			return err
		}
		var res protocol.TemplateLaunchResult
		if err := c.Call(protocol.MethodTemplateLaunch, protocol.TemplateLaunchParams{
			WorkspaceID: wsID, Name: name, Params: params,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("run %s %s (template %s)\n", res.Run.ID, res.Run.Status, name)
		if res.BaseAge == "" {
			fmt.Printf("base %s: no commit the server has seen; push it to refresh\n", res.BaseBranch)
		} else {
			fmt.Printf("base %s is %s old (the server only sees what members push)\n", res.BaseBranch, res.BaseAge)
		}
		return nil
	})
}

func formatParams(params map[string]string) string {
	if len(params) == 0 {
		return "-"
	}
	names := slices.Sorted(maps.Keys(params))
	return strings.Join(names, ",")
}
