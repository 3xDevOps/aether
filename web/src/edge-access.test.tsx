import { render, screen } from '@testing-library/react'
import type * as apiModule from '@/lib/api'
import { App } from '@/App'
import { ApiError } from '@/lib/api'
import { useStore } from '@/store'
import { StubSocket } from '@/test/stub-socket'

// What the gateway answers the capabilities probe with; each test sets it.
const probe = vi.hoisted(() => ({ refusal: null as unknown }))

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return {
    ...actual,
    api: fakeApi({ capabilities: vi.fn(() => Promise.reject(probe.refusal)) }),
  }
})

beforeAll(() => {
  StubSocket.install()
})
afterAll(() => vi.unstubAllGlobals())

beforeEach(() => {
  useStore.getState().resetConnection()
})

describe('App on the edge gateway', () => {
  it('replaces the shell with the sign-in page when the API answers 401', async () => {
    probe.refusal = new ApiError(401, '/capabilities: sign in required', -32001, {
      login: '/auth/login',
    })
    render(<App />)

    const signIn = await screen.findByRole('link', { name: 'Sign in' })
    expect(signIn.getAttribute('href')).toBe('/auth/login')
    expect(screen.queryByRole('complementary', { name: 'Runs' })).toBeNull()
    expect(screen.queryByText(/aether gui/)).toBeNull()
  })

  it('shows the approval page to a browser waiting for approval', async () => {
    probe.refusal = new ApiError(403, '/capabilities: device is pending approval', -32001, {
      approval_code: 'WXYZ-2345',
    })
    render(<App />)

    expect(
      await screen.findByRole('heading', { name: 'This browser is waiting for approval' }),
    ).toBeDefined()
    expect(screen.getByText('aether device approve WXYZ-2345')).toBeDefined()
    expect(screen.queryByRole('complementary', { name: 'Runs' })).toBeNull()
  })

  it('still reports an expired local token as an expired link, not a sign-in', async () => {
    probe.refusal = new ApiError(
      401,
      'a valid gateway token is required; restart `aether gui` for a fresh URL',
      -32001,
    )
    render(<App />)

    expect(
      await screen.findByRole('heading', { name: 'This dashboard link has expired' }),
    ).toBeDefined()
    expect(screen.queryByRole('link', { name: 'Sign in' })).toBeNull()
  })
})
