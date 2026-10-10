// The format `aether workspace env import` reads; cmd/aether/workspace_env.go
// holds the same rules and the same test cases.

export interface DotenvResult {
  /** In file order; a later line replaces an earlier one of the same name. */
  variables: { name: string; value: string }[]
  /** 1-based numbers of the lines that are not NAME=VALUE. */
  badLines: number[]
}

const escapes: Record<string, string> = { '\\n': '\n', '\\r': '\r', '\\t': '\t', '\\"': '"', '\\\\': '\\' }

function value(raw: string): string {
  if (raw.length >= 2) {
    const quote = raw[0]
    if (quote === raw[raw.length - 1]) {
      if (quote === "'") return raw.slice(1, -1)
      if (quote === '"') return raw.slice(1, -1).replace(/\\[nrt"\\]/g, (escape) => escapes[escape])
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
