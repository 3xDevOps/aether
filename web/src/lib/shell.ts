// The characters every POSIX shell passes through untouched. The set is
// deliberately conservative: anything outside it, non-ASCII included,
// gets quoted rather than reasoned about.
const literal = /^[A-Za-z0-9_@%+=:,./-]+$/

/**
 * A string as one POSIX shell argument, following the same rule as
 * Python's shlex.quote: plain strings stay readable and unquoted,
 * everything else is single quoted with embedded quotes spliced out.
 * The dashboard prints commands for the user to paste, and those carry
 * profile paths the user chose the names of.
 */
export function shellQuote(value: string): string {
  if (literal.test(value)) return value
  return `'${value.replaceAll("'", `'\\''`)}'`
}

/**
 * A relative path as one shell argument a flag parser reads as a value.
 * Quoting alone cannot do it: the shell strips the quotes and the parser
 * still sees a name starting with a dash, so such a path is written
 * relative to the current directory instead.
 */
export function shellPath(path: string): string {
  return shellQuote(path.startsWith('-') ? `./${path}` : path)
}
