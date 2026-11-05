import { syncAppBadge } from '@/lib/app-badge'

function badging(subscribed: boolean) {
  const nav = {
    setAppBadge: vi.fn(async () => {}),
    clearAppBadge: vi.fn(async () => {}),
    serviceWorker: { getRegistration: async () => ({ pushManager: { getSubscription: async () => (subscribed ? {} : null) } }) },
  }
  vi.stubGlobal('navigator', nav)
  return nav
}

afterEach(() => vi.unstubAllGlobals())

describe('syncAppBadge', () => {
  it('shows the Needs you count while push is on', async () => {
    const nav = badging(true)
    await syncAppBadge(3)
    expect(nav.setAppBadge).toHaveBeenCalledWith(3)
    await syncAppBadge(0)
    expect(nav.clearAppBadge).toHaveBeenCalledTimes(1)
  })

  it('clears the badge without a push subscription, and does nothing without the API', async () => {
    const nav = badging(false)
    await syncAppBadge(3)
    expect(nav.setAppBadge).not.toHaveBeenCalled()
    expect(nav.clearAppBadge).toHaveBeenCalledTimes(1)
    vi.stubGlobal('navigator', {})
    await expect(syncAppBadge(3)).resolves.toBeUndefined()
  })
})
