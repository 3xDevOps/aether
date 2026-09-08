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
