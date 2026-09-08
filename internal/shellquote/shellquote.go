// Package shellquote turns a string into a single POSIX shell argument.
// Aether prints commands for the user to paste, and those commands carry
// profile paths the user chose the names of; a space or shell syntax in
// one would otherwise change what the pasted command runs.
package shellquote

import "strings"

// literal are the characters every POSIX shell passes through untouched.
// The set is deliberately conservative: anything outside it, non-ASCII
// included, gets quoted rather than reasoned about.
const literal = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_@%+=:,./-"

// Quote returns s as one shell argument, following the same rule as
// Python's shlex.quote: plain strings stay readable and unquoted,
// everything else is single quoted with embedded quotes spliced out.
func Quote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !strings.ContainsRune(literal, r) }) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
