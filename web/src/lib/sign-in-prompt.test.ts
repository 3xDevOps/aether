import { readSignInPrompt } from '@/lib/sign-in-prompt'

const claudeURL = 'https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference&code_challenge=challenge&code_challenge_method=S256&state=state'

// What claude-agent-acp 0.86.0 --cli auth login prints on a PTY: the URL as
// an OSC 8 link around its colored text.
const claude = `Opening browser to sign in…\r\nIf the browser didn't open, visit: \x1b]8;;${claudeURL}\x07\x1b[94m${claudeURL}\x1b[39m\x1b]8;;\x07\r\nPaste code here if prompted > `

// codex login --device-auth, from device_code_prompt in codex-rs/login.
const codex = '\nWelcome to Codex [v\x1b[90m0.130.0\x1b[0m]\n\x1b[90mOpenAI\'s command-line coding agent\x1b[0m\n\nFollow these steps to sign in with ChatGPT using device code authorization:\n\n1. Open this link in your browser and sign in to your account\n   \x1b[94mhttps://auth.openai.com/codex/device\x1b[0m\n\n2. Enter this one-time code \x1b[90m(expires in 15 minutes)\x1b[0m\n   \x1b[94mWXYZ-12345\x1b[0m\n\n\x1b[90mContinue only if you started this login in Codex. If a website or another person gave you this code, cancel.\x1b[0m\n'

describe('readSignInPrompt', () => {
  it('reads the Claude sign-in URL out of its terminal link', () => {
    expect(readSignInPrompt(claude, 'claude')).toEqual({ url: claudeURL, code: undefined })
  })

  it('reads the link target of a URL wrapped across lines', () => {
    const wrapped = claude.replace(`\x1b[94m${claudeURL}`, `\x1b[94m${claudeURL.slice(0, 40)}\r\n${claudeURL.slice(40)}`)
    expect(readSignInPrompt(wrapped, 'claude').url).toBe(claudeURL)
  })

  it('reads the Codex device URL and code', () => {
    expect(readSignInPrompt(codex, 'codex')).toEqual({ url: 'https://auth.openai.com/codex/device', code: 'WXYZ-12345' })
  })

  it("offers only the vendor's sign-in host, and the newest URL", () => {
    expect(readSignInPrompt('See https://example.com/claude for help\n', 'claude')).toEqual({ url: undefined, code: undefined })
    const retried = `${claude}\r\nInvalid code. Visit: \x1b]8;;${claudeURL}2\x07${claudeURL}2\x1b]8;;\x07\r\n`
    expect(readSignInPrompt(retried, 'claude').url).toBe(`${claudeURL}2`)
  })

  it('waits for the rest of a URL or code cut by a chunk', () => {
    expect(readSignInPrompt(claude.slice(0, claude.indexOf('&state')), 'claude')).toEqual({})
    expect(readSignInPrompt(codex.slice(0, codex.indexOf('-12345')), 'codex')).toEqual({ url: 'https://auth.openai.com/codex/device' })
  })
})
