import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Api } from '@/lib/api'
import { InsightsRoute } from '@/routes/insights'
import { useStore } from '@/store'
import { alice, fakeApi, serverInfo, systemContainer, systemStats, workspace } from '@/test/fixtures'

function renderSystem(over: Partial<Api> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    info: { ...serverInfo, member: alice },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    connection: 'live',
    hydrated: true,
    route: { name: 'insights', params: { tab: 'system' } },
  })
  const client = fakeApi({ systemStats: vi.fn(async () => systemStats()), ...over })
  render(<InsightsRoute params={{ tab: 'system' }} client={client} />)
  return client
}

const rows = () => within(screen.getByRole('list', { name: 'Containers by usage' })).getAllByRole('listitem')

describe('system tab', () => {
  it('shows six tile placeholders and keeps the sentence for assistive technology', async () => {
    renderSystem({ systemStats: vi.fn(() => new Promise<never>(() => {})) })

    const sentence = await screen.findByText('Reading system stats…')
    expect(sentence.parentElement!.querySelectorAll('[data-slot="skeleton"]')).toHaveLength(6)
  })

  it('shows the host figures as tiles', async () => {
    const client = renderSystem()
    const cpu = await screen.findByRole('group', { name: 'CPU' })

    expect(cpu.textContent).toContain('41%')
    expect(cpu.textContent).toContain('8 cores · load 3.25')
    const memory = screen.getByRole('group', { name: 'Memory' })
    expect(memory.textContent).toContain('50%')
    expect(memory.textContent).toContain('8.0 GB of 16 GB · swap 0 B of 2.0 GB')
    expect(screen.getByRole('group', { name: 'Disk' }).textContent).toContain('25%')
    expect(screen.getByRole('group', { name: 'Network' }).textContent).toContain('↓ 117 KB/s ↑ 29 KB/s')
    expect(screen.getByRole('group', { name: 'Containers' }).textContent).toContain('9 running8 sessions · 1 environment · up 3d 4h')
    expect(screen.getByRole('group', { name: 'Pressure' }).textContent).toContain('CPU 2.0% · Memory 0.0% · I/O 0.5%')
    expect(screen.getByRole('status').textContent).toBe('Live · every 5s')
    expect(client.systemStats).toHaveBeenCalledWith({ since_ms: 0 }, expect.any(AbortSignal))
  })

  it('filters the ranked list to the Other runs slice and clears it', async () => {
    renderSystem()
    await screen.findByRole('list', { name: 'Containers by usage' })
    expect(rows()).toHaveLength(9)
    expect(rows()[0]!.textContent).toContain('rewrite the checkout flow')

    fireEvent.click(within(screen.getByRole('list', { name: 'Slices' })).getByRole('button', { name: /Other sessions/ }))

    expect(screen.getByText('Showing 7 sessions in Other sessions')).toBeDefined()
    expect(rows()).toHaveLength(7)
    expect(screen.queryByText('Aether server, Docker and host processes')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Show all' }))
    expect(rows()).toHaveLength(9)
  })

  it('opens the run behind a row', async () => {
    renderSystem()
    await screen.findByRole('list', { name: 'Containers by usage' })

    fireEvent.click(within(rows()[0]!).getByRole('button'))

    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
  })

  it("opens only the viewer's own environment", async () => {
    renderSystem()
    await screen.findByRole('list', { name: 'Containers by usage' })
    expect(rows()[1]!.textContent).toContain('Environment container')
    expect(within(rows()[1]!).queryByRole('button')).toBeNull()
    cleanup()

    renderSystem({
      systemStats: vi.fn(async () => systemStats({
        containers: [systemContainer({ owner_kind: 'member', owner_id: alice.id, member_id: alice.id, title: undefined, status: undefined })],
      })),
    })
    await screen.findByRole('list', { name: 'Containers by usage' })
    fireEvent.click(within(rows()[0]!).getByRole('button'))
    expect(useStore.getState().route).toEqual({ name: 'environment', params: {} })
  })

  it('starts over when the signed-in identity changes', async () => {
    const client = renderSystem()
    await screen.findByRole('list', { name: 'Containers by usage' })
    const read = vi.mocked(client.systemStats)
    read.mockClear()
    read.mockImplementation(async () => systemStats({ containers: [], others: undefined }))

    act(() => useStore.setState({ identityKey: 'another-server' }))

    expect(await screen.findByText('No run containers are running.')).toBeDefined()
    expect(read).toHaveBeenCalledWith({ since_ms: 0 }, expect.any(AbortSignal))
  })

  it('says so when the server cannot report stats, and still shows storage', async () => {
    renderSystem({ systemStats: vi.fn(async () => { throw new ApiError(404, 'system.stats: not found') }) })

    expect(await screen.findByText('This server does not report system stats. Update the server to see them.')).toBeDefined()
    expect(screen.getByRole('region', { name: 'Storage' })).toBeDefined()
  })

  it('keeps the last data and shows the real error when a poll fails', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      let calls = 0
      renderSystem({
        systemStats: vi.fn(async () => {
          if (calls++ === 0) return systemStats()
          throw new Error('system.stats: gateway timeout')
        }),
      })
      await act(() => vi.advanceTimersByTimeAsync(0))
      expect(screen.getByRole('group', { name: 'CPU' })).toBeDefined()

      await act(() => vi.advanceTimersByTimeAsync(5000))

      expect(screen.getByRole('status').textContent).toBe('system.stats: gateway timeout')
      expect(screen.getByRole('group', { name: 'CPU' })).toBeDefined()
    } finally {
      vi.useRealTimers()
    }
  })
})
