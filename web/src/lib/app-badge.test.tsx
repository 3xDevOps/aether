import { act, render, renderHook } from '@testing-library/react'
import { AppBadge, syncAppBadge, useBadging } from '@/lib/app-badge'
import { pushChanged } from '@/lib/push'
import { useAttention } from '@/routes/needs-you/attention'
import type { AttentionItem } from '@/lib/types'

function badging(subscribed: boolean) {
  const nav = {
    setAppBadge: vi.fn(async () => {}),
    clearAppBadge: vi.fn(async () => {}),
    serviceWorker: { getRegistration: async () => ({ pushManager: { getSubscription: async () => (subscribed ? {} : null) } }) },
    subscribed,
  }
  vi.stubGlobal('navigator', nav)
  return nav
}

const asks = (n: number): AttentionItem[] =>
  Array.from({ length: n }, (_, i) => ({ kind: 'permission', run_id: `r${i}`, workspace_id: 'w', title: 't', harness: 'claude', reason: '' }) as AttentionItem)

afterEach(() => {
  vi.unstubAllGlobals()
  useAttention.setState({ items: null })
})

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

  it('rejects with context when the browser refuses', async () => {
    const nav = badging(true)
    nav.setAppBadge.mockRejectedValue(new Error('denied'))
    await expect(syncAppBadge(1)).rejects.toThrow('set the app badge to 1: denied')
  })
})

describe('syncAppBadge ordering', () => {
  it('lets a newer sync win over an older one still reading the subscription', async () => {
    const nav = badging(true)
    let release: (value: object) => void = () => {}
    nav.serviceWorker.getRegistration = async () => ({ pushManager: { getSubscription: () => new Promise((resolve) => { release = resolve }) } })
    const older = syncAppBadge(5)
    nav.serviceWorker.getRegistration = async () => ({ pushManager: { getSubscription: async () => ({}) } })
    await syncAppBadge(0)
    release({})
    await older
    expect(nav.setAppBadge).not.toHaveBeenCalled()
    expect(nav.clearAppBadge).toHaveBeenCalledTimes(1)
  })
})

describe('AppBadge', () => {
  it('follows the Needs you count in any layout, re-syncs when push changes, and clears on unmount', async () => {
    const nav = badging(true)
    useAttention.setState({ items: asks(2) })
    const view = render(<AppBadge />)
    await vi.waitFor(() => expect(nav.setAppBadge).toHaveBeenLastCalledWith(2))

    act(() => useAttention.setState({ items: asks(3) }))
    await vi.waitFor(() => expect(nav.setAppBadge).toHaveBeenLastCalledWith(3))

    nav.subscribed = false
    nav.serviceWorker.getRegistration = async () => ({ pushManager: { getSubscription: async () => null } })
    act(() => void window.dispatchEvent(new Event(pushChanged)))
    await vi.waitFor(() => expect(nav.clearAppBadge).toHaveBeenCalledTimes(1))

    view.unmount()
    await vi.waitFor(() => expect(nav.clearAppBadge).toHaveBeenCalledTimes(2))
  })
})

describe('useBadging', () => {
  it('is on only while this browser is subscribed, so a push-off browser reads no attention list', async () => {
    const nav = badging(false)
    const { result } = renderHook(() => useBadging())
    await act(async () => {})
    expect(result.current).toBe(false)

    nav.serviceWorker.getRegistration = async () => ({ pushManager: { getSubscription: async () => ({}) } })
    await act(async () => void window.dispatchEvent(new Event(pushChanged)))
    expect(result.current).toBe(true)

    vi.stubGlobal('navigator', {})
    const unsupported = renderHook(() => useBadging())
    await act(async () => {})
    expect(unsupported.result.current).toBe(false)
  })
})
