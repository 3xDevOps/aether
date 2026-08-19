// Command aether is the Aether CLI: launches and steers runs, syncs git
// branches, and manages agent profiles against an aether-server.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/version"
)

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aether:", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		printHelp(os.Stdout)
		return nil
	case "version":
		fmt.Println("aether", version.String())
		return nil
	case "daemon":
		return daemonCmd(args[1:])
	}
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(args[1:])
		}
	}
	printHelp(os.Stderr)
	return fmt.Errorf("unknown command %q", args[0])
}
func parseLeadingArg(fs *flag.FlagSet, args []string) (string, error) {
	var leading string
	if len(args) > 0 && args[0] != "--" && !strings.HasPrefix(args[0], "-") {
		leading, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if leading != "" && fs.NArg() == 0 {
		return leading, nil
	}
	if leading == "" && fs.NArg() == 1 {
		return fs.Arg(0), nil
	}
	return "", fmt.Errorf("expected exactly one positional argument")
}

func printHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: aether <command> [args]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "commands:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	type row struct{ name, short string }
	rows := []row{
		{name: "daemon", short: "run or install the local git sync daemon"},
		{name: "version", short: "print the version"},
	}
	for _, c := range commands {
		rows = append(rows, row{c.name, c.short})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", r.name, r.short)
	}
	_ = tw.Flush()
}

func helpText() string {
	var b strings.Builder
	printHelp(&b)
	return b.String()
}
