// The format `aether workspace env import` reads; cmd/aether/workspace_env.go
// holds the same rules and the same test cases.

export interface DotenvResult {
  /** In file order; a later line replaces an earlier one of the same name. */
  variables: { name: string; value: string }[]
  /** 1-based numbers of the lines that are not NAME=VALUE. */
  badLines: number[]
}

const escapes: Record<string, string> = { '\\n': '\n', '\\r': '\r', '\\t': '\t', '\\"': '"', '\\\\': '\\' }

function closingQuote(raw: string): number {
  for (let i = 1; i < raw.length; i++) {
    if (raw[0] === '"' && raw[i] === '\\') i++
    else if (raw[i] === raw[0]) return i
  }
  return -1
}

function value(raw: string): string {
  if (raw[0] === '"' || raw[0] === "'") {
    const end = closingQuote(raw)
    const rest = raw.slice(end + 1).trim()
    if (end > 0 && (rest === '' || rest[0] === '#')) {
      const inner = raw.slice(1, end)
      return raw[0] === '"' ? inner.replace(/\\[nrt"\\]/g, (escape) => escapes[escape]) : inner
    }
  }
  const comment = raw.indexOf(' #')
  return comment >= 0 ? raw.slice(0, comment).trim() : raw
}

export function parseDotenv(text: string): DotenvResult {
  const variables = new Map<string, string>()
  const badLines: number[] = []
  text.split(/\r?\n/).forEach((raw, index) => {
    const line = raw.trim()
    if (line === '' || line.startsWith('#')) return
    const pair = line.startsWith('export ') ? line.slice('export '.length) : line
    const equals = pair.indexOf('=')
    const name = equals < 0 ? '' : pair.slice(0, equals).trim()
    if (name === '' || /[ \t]/.test(name)) {
      badLines.push(index + 1)
      return
    }
    variables.set(name, value(pair.slice(equals + 1).trim()))
  })
  return { variables: [...variables].map(([name, v]) => ({ name, value: v })), badLines }
}
