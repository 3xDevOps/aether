// Reads what an agent's sign-in asks for from its terminal output: the URL
// `claude-agent-acp --cli auth login` and `codex login --device-auth` print,
// and the one-time code Codex prints under it.

const escapes = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]/g

export interface SignInPrompt {
  url?: string
  /** Codex's device code, entered on the page the URL opens. */
  code?: string
}

export function readSignInPrompt(output: string): SignInPrompt {
  const text = output.replace(escapes, '')
  // A link's target survives the CLI wrapping its text. A URL is whole only
  // once something follows it; output arrives in chunks.
  const url = /\x1b\]8;[^;\x07\x1b]*;(https:\/\/[^\x07\x1b]+)(?:\x07|\x1b\\)/.exec(output)?.[1] ?? /https:\/\/\S+(?=\s)/.exec(text)?.[0]
  const code = /one-time code[^\n]*\n\s*(\S+)\s/.exec(text)?.[1]
  return { url, code }
}
