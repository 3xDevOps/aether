import { renderHook } from '@testing-library/react'
import { api } from '@/lib/api'
import { useAttentionFeed } from '@/routes/needs-you/attention'
import { useStore } from '@/store'

vi.mock('@/lib/api', async (original) => ({
  ...(await original<typeof import('@/lib/api')>()),
  api: { attentionList: vi.fn(async () => ({ items: [] })) },
}))

describe('useAttentionFeed', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
  })
  afterEach(() => vi.useRealTimers())

  it('reads the list once however many feeds are mounted', async () => {
    renderHook(() => {
      useAttentionFeed()
      useAttentionFeed()
    })
    await vi.advanceTimersByTimeAsync(300)
    expect(api.attentionList).toHaveBeenCalledTimes(1)
  })
})
