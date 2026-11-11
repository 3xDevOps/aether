import { asFallback, readiness } from '@/components/launch/account-picker'
import { agentInfo } from '@/test/fixtures'
import type { UsageProvider } from '@/lib/types'

const now = Date.parse('2026-11-06T12:00:00Z')
const usage = (used_percent: number, status: UsageProvider['status'] = 'ok'): UsageProvider[] => [
  { provider: 'claude', status, checked_at: '2026-11-06T11:59:00Z', windows: [{ id: '5h', label: '5 hours', used_percent }], ...(status === 'stale' ? { error: 'vendor timed out' } : {}) },
]
const claude = (over: Parameters<typeof agentInfo>[0] = {}) => agentInfo({ name: 'claude', login_found: true, ...over })
const hardLink = 'login file has another hard link'
const signedOut = '~/.claude/.credentials.json: Claude OAuth access token is missing or invalid'

describe('readiness', () => {
  it.each([
    ['Not installed', { agents: [claude({ installed: false })] }, { className: 'text-muted', refused: true }],
    ['Not installed', { agents: [] }, { className: 'text-muted', refused: true }],
    ["Needs Bob's login", { agents: [claude({ login_missing: true })], usage: usage(10) }, { className: 'text-state-needs-you', refused: true, title: "Needs Bob's login" }],
    ['Your account only', { agents: [claude({ own_account_only: true })] }, { className: 'text-muted', refused: true }],
    ['Unavailable', { agents: [claude({ unavailable: hardLink })] }, { className: 'text-state-failed', title: hardLink, refused: true }],
    ['Sign in after launch', { agents: [claude({ login_found: false })] }, { className: 'text-muted', signIn: true }],
    ['Signed out', { agents: [claude({ login_problem: signedOut, login_blocks_launch: true })], usage: usage(10) }, { className: 'text-state-needs-you', title: signedOut }],
    ['Ready', { agents: [claude()] }, { className: 'text-text' }],
    ['Ready', { agents: [claude()], usageError: 'account.usage: usage providers unavailable' }, { className: 'text-text', title: 'Usage not read: account.usage: usage providers unavailable' }],
    ['Not checked', { agentsError: 'agent.list: connection refused' }, { className: 'text-state-failed', title: 'agent.list: connection refused' }],
    ['40% used', { agents: [claude()], usage: usage(40) }, { className: 'text-text' }],
    ['85% used', { agents: [claude()], usage: usage(85) }, { className: 'text-state-needs-you' }],
    ['100% used', { agents: [claude()], usage: usage(100) }, { className: 'text-state-failed' }],
    ['40% used · stale', { agents: [claude()], usage: usage(40, 'stale') }, { className: 'text-text', title: 'vendor timed out' }],
  ])('says %s', (text, status, rest) => {
    expect(readiness(status, 'claude', 'Bob', now)).toEqual({ text, title: undefined, ...rest })
  })

  it('says nothing until the account\'s agent list answers, even with usage known', () => {
    expect(readiness({ usage: usage(50) }, 'claude', 'Bob', now)).toBeNull()
    expect(readiness(undefined, 'claude', 'Bob', now)).toBeNull()
  })

  it('says on a fallback row which accounts usage fallback skips', () => {
    expect(asFallback(readiness({ agents: [claude({ login_missing: true })] }, 'claude', 'Bob', now))?.text).toBe("Skipped: needs Bob's login")
    expect(asFallback(readiness({ agents: [claude({ unavailable: hardLink })] }, 'claude', 'Bob', now))).toMatchObject({ text: 'Skipped: unavailable', className: 'text-muted', title: hardLink })
    expect(asFallback(readiness({ agents: [claude({ login_found: false })] }, 'claude', undefined, now))).toMatchObject({ text: 'No saved login', title: expect.stringContaining('A running session moves here only if an API key signs the agent in.') })
    expect(asFallback(readiness({ agents: [claude()] }, 'claude', undefined, now))?.text).toBe('Ready')
  })

  it('clips a long owner name in the status and keeps it whole in the title', () => {
    const owner = 'o'.repeat(64)
    expect(readiness({ agents: [claude({ login_missing: true })] }, 'claude', owner, now)).toMatchObject({ text: `Needs ${'o'.repeat(12)}…'s login`, title: `Needs ${owner}'s login` })
  })

  it('says nothing for the pinned custom agent', () => {
    expect(readiness({ agents: [claude()] }, 'custom', 'Bob', now)).toBeNull()
  })
})
