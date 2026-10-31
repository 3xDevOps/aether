package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
)

// acpKeeper is an internal entrypoint of the staged binary, including when
// its basename is aether-internal: the keeper of an enhanced run's ACP
// adapter or of a development terminal, and the bridge a server attaches to
// either through.
func acpKeeper(args []string) int {
	switch {
	case len(args) >= 4 && args[0] == "serve" && args[2] == "--":
		return acpkeeper.Serve(args[1], "", args[3:])
	case len(args) >= 6 && args[0] == "serve" && args[2] == "--user" && args[4] == "--":
		return acpkeeper.Serve(args[1], args[3], args[5:])
	case len(args) >= 5 && args[0] == "term" && args[3] == "--":
		if cols, rows, ok := termSize(args[2]); ok {
			return acpkeeper.ServeTerm(args[1], "", "", cols, rows, args[4:])
		}
	case len(args) >= 9 && args[0] == "term" && args[3] == "--user" && args[5] == "--home" && args[7] == "--":
		if cols, rows, ok := termSize(args[2]); ok {
			return acpkeeper.ServeTerm(args[1], args[4], args[6], cols, rows, args[8:])
		}
	case len(args) == 2 && args[0] == "attach":
		if err := acpkeeper.Bridge(args[1], os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "acp-keeper:", err)
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintln(os.Stderr, "acp-keeper: expected serve <socket> [--user <uid:gid>] -- <argv...>, term <socket> <cols>x<rows> [--user <uid:gid> --home <dir>] -- <argv...> or attach <socket>")
	return 125
}

func termSize(s string) (cols, rows uint16, ok bool) {
	c, r, found := strings.Cut(s, "x")
	cn, cerr := strconv.ParseUint(c, 10, 16)
	rn, rerr := strconv.ParseUint(r, 10, 16)
	if !found || cerr != nil || rerr != nil || cn == 0 || rn == 0 {
		return 0, 0, false
	}
	return uint16(cn), uint16(rn), true
}
