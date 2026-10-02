import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { UsageReader } from '@/components/shell/usage'
import { ApiError, type Api } from '@/lib/api'
import type { UsageResult } from '@/lib/types'
import { alice, bob, serverInfo } from '@/test/fixtures'
import { pickOption } from '@/test/select'
import { useStore } from '@/store'

function seed() {
  useStore.setState({
    info: { ...serverInfo, member: alice },
    connection: 'live',
    hydrated: true,
    capabilities: null,
    unreachable: null,
  })
}

function usage(accountID: string, used = 25, reset = '2099-01-01T00:00:00Z'): UsageResult {
  return {
    account_member_id: accountID,
    providers: [
      {
        provider: 'claude',
        status: 'ok',
        windows: [{ id: 'five-hour', label: 'Five hour', used_percent: used, resets_at: reset }],
        checked_at: '2026-09-18T10:00:00Z',
        updated_at: '2026-09-18T10:00:00Z',
      },
      {
        provider: 'codex',
        status: 'unsupported',
        windows: [],
        checked_at: '2026-09-18T10:00:00Z',
      },
    ],
  }
}

function client(over: Partial<Api> = {}): Api {
  return {
    accountList: vi.fn(async () => ({ accounts: [alice, bob], shared_with: [] })),
    accountUsage: vi.fn(async () => usage(alice.id)),
    ...over,
  } as Api
}

describe('subscription usage reader', () => {
  test('discards a response for an account that is no longer selected', async () => {
    seed()
    const aliceDeferred = Promise.withResolvers<UsageResult>()
    const bobDeferred = Promise.withResolvers<UsageResult>()
    let calls = 0
    const accountUsage = vi.fn(
      (params: { account_member_id?: string; refresh?: boolean } = {}) => {
        calls += 1
        return params.account_member_id === bob.id || calls > 1
          ? bobDeferred.promise
          : aliceDeferred.promise
      },
    )
    const api = client({ accountUsage })
    render(<UsageReader client={api} />)

    fireEvent.click(screen.getByRole('button', { name: 'Usage' }))
    const accountTrigger = screen.getByLabelText('Account')
    await waitFor(() => expect(accountTrigger.textContent).toContain('Alice (you)'))
    await pickOption(accountTrigger, 'Bob')
    await waitFor(() =>
      expect(accountUsage).toHaveBeenCalledWith(
        expect.objectContaining({ account_member_id: bob.id }),
      ),
    )

    aliceDeferred.resolve(usage(alice.id, 90))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.queryByText('90% used')).toBeNull()

    bobDeferred.resolve(usage(bob.id, 40))
    expect(await screen.findByText('40% used')).toBeTruthy()
  })
  test('pauses unsupported background retries but lets Refresh try again', async () => {
    seed()
    const accountUsage = vi.fn(async () => {
      throw new ApiError(404, 'account.usage: method not found', -32601)
    })
    const api = client({ accountUsage })
    render(<UsageReader client={api} />)
    await waitFor(() => expect(accountUsage).toHaveBeenCalledTimes(1))

    fireEvent.click(screen.getByRole('button', { name: 'Usage' }))
    expect(
      await screen.findByText(
        'This server does not provide account usage. Update the server to enable subscription monitoring.',
      ),
    ).toBeTruthy()
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
    expect(accountUsage).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole('button', { name: 'Refresh usage' }))
    await waitFor(() => expect(accountUsage).toHaveBeenCalledTimes(2))
  })


  test('does not present an expired window as current', async () => {
    seed()
    const api = client({ accountUsage: vi.fn(async () => usage(alice.id, 99, '2020-01-01T00:00:00Z')) })
    render(<UsageReader client={api} />)
    fireEvent.click(screen.getByRole('button', { name: 'Usage' }))

    expect(await screen.findByText('No current measured windows.')).toBeTruthy()
    expect(screen.queryByRole('progressbar', { name: /Claude Five hour usage/ })).toBeNull()
  })

  test('keeps the last values but marks them stale after transport failure', async () => {
    seed()
    const accountUsage = vi
      .fn()
      .mockResolvedValueOnce(usage(alice.id, 35))
      .mockRejectedValueOnce(new Error('connection closed'))
    const api = client({ accountUsage })
    render(<UsageReader client={api} />)
    fireEvent.click(screen.getByRole('button', { name: 'Usage' }))
    expect(await screen.findByText('35% used')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Refresh usage' }))
    expect(await screen.findByText('Stale values are from the last successful check: connection closed')).toBeTruthy()
    expect(screen.getByText('35% used')).toBeTruthy()
  })

  test('opens and closes the usage popover from the keyboard', async () => {
    seed()
    render(<UsageReader client={client()} />)
    const trigger = screen.getByRole('button', { name: 'Usage' })
    trigger.focus()
    await userEvent.keyboard('{Enter}')
    expect(await screen.findByRole('heading', { name: 'Subscription usage' })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Subscription usage' })).toBeNull())
    expect(document.activeElement).toBe(trigger)
  })
})
