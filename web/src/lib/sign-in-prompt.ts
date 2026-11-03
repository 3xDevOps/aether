// Reads what an agent's sign-in asks for from its terminal output: the URL
// `claude-agent-acp --cli auth login` and `codex login --device-auth` print,
// and the one-time code Codex prints under it.

const escapes = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]/g

export interface SignInPrompt {
  url?: string
  /** Codex's device code, entered on the page the URL opens. */
  code?: string
}

// The vendors' own sign-in hosts: a button opens only those.
const hosts: Record<string, string[]> = { claude: ['claude.com', 'claude.ai', 'anthropic.com'], codex: ['openai.com', 'chatgpt.com'] }

function vendorURL(agent: string, raw: string): boolean {
  try {
    const host = new URL(raw).hostname
    return (hosts[agent] ?? []).some((h) => host === h || host.endsWith(`.${h}`))
  } catch {
    return false
  }
}

export function readSignInPrompt(output: string, agent: string): SignInPrompt {
  const text = output.replace(escapes, '')
  // A link's target survives the CLI wrapping its text. A URL is whole only
  // once something follows it; output arrives in chunks. The last one wins,
  // since a retried sign-in prints a new one.
  const links = [...output.matchAll(/\x1b\]8;[^;\x07\x1b]*;(https:\/\/[^\x07\x1b]+)(?:\x07|\x1b\\)/g)].map((m) => m[1]!)
  const plain = [...text.matchAll(/https:\/\/\S+(?=\s)/g)].map((m) => m[0])
  const url = (links.length ? links : plain).filter((u) => vendorURL(agent, u)).at(-1)
  const code = /one-time code[^\n]*\n\s*(\S+)\s/.exec(text)?.[1]
  return { url, code }
}
