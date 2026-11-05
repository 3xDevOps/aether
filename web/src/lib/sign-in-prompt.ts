// Reads what an agent's sign-in asks for from its terminal output: the URL
// `claude-agent-acp --cli auth login` and `codex login --device-auth` print,
// and the one-time code Codex prints under it.

const escapes = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]/g

export interface SignInPrompt {
  url?: string
  /** The URL came from a terminal link, which keeps it whole when the screen wraps it. */
  linked?: boolean
  /** Codex's device code, entered on the page the URL opens. */
  code?: string
}

// The vendors' own sign-in hosts: a button opens only those.
const hosts: Record<string, string[]> = { claude: ['claude.com', 'claude.ai', 'anthropic.com'], codex: ['openai.com', 'chatgpt.com'] }

/** The last line the sign-in printed, without escapes. */
export function lastLine(output: string): string {
  return output.replace(escapes, '').split(/\r?\n|\r/).map((line) => line.trim()).filter(Boolean).at(-1) ?? ''
}

export function readSignInPrompt(output: string, agent: string): SignInPrompt {
  const text = output.replace(escapes, '')
  // A link's target survives the CLI wrapping its text. A URL is whole only
  // once something follows it; output arrives in chunks. The last one wins,
  // since a retried sign-in prints a new one.
  const links = [...output.matchAll(/\x1b\]8;[^;\x07\x1b]*;(https:\/\/[^\x07\x1b]+)(?:\x07|\x1b\\)/g)].map((m) => m[1]!)
  const plain = [...text.matchAll(/https:\/\/\S+(?=\s)/g)].map((m) => m[0])
  const url = (links.length ? links : plain).filter((u) => {
    const host = URL.canParse(u) ? new URL(u).hostname : ''
    return (hosts[agent] ?? []).some((h) => host === h || host.endsWith(`.${h}`))
  }).at(-1)
  const linked = links.length > 0 && url !== undefined
  const code = /one-time code[^\n]*\n\s*(\S+)\s/.exec(text)?.[1]
  return linked ? { url, linked, code } : { url, code }
}
