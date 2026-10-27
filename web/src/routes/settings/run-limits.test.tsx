import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { ServerRunLimits } from '@/lib/types'
import { ServerSection } from '@/routes/settings/server'
import { useStore } from '@/store'
import { alice, serverInfo } from '@/test/fixtures'

const gib = 2 ** 30
const initial: ServerRunLimits = { limits: {}, default: { cpus: 8, memory_bytes: 8 * gib, pids: 4096 }, effective: { cpus: 8, memory_bytes: 8 * gib, pids: 4096 } }

function seed() {
  useStore.setState({
    info: { ...serverInfo, member: alice },
    connection: 'live',
    hydrated: true,
    capabilities: { gateway: 'remote', methods: ['server.info', 'server.run_limits'], ws: [] },
  })
}

describe('Run limits row', () => {
  afterEach(cleanup)

  test('saves the limits an admin types and shows what runs now get', async () => {
    seed()
    const serverRunLimits = vi.fn(async (params?: Parameters<Api['serverRunLimits']>[0]) =>
      params?.memory_bytes === undefined
        ? initial
        : { limits: { memory_bytes: params.memory_bytes }, default: initial.default, effective: { cpus: 8, memory_bytes: params.memory_bytes, pids: 4096 } })
    const client = { serverRunLimits, serverInfo: vi.fn(async () => serverInfo) } as unknown as Api
    render(<ServerSection client={client} />)
    await act(async () => {})

    expect((screen.getByLabelText('Memory (GiB)') as HTMLInputElement).placeholder).toBe('8')
    const save = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Memory (GiB)'), { target: { value: '2' } })
    expect(save.disabled).toBe(false)
    fireEvent.click(save)
    await act(async () => {})

    expect(serverRunLimits).toHaveBeenLastCalledWith({ cpus: 0, memory_bytes: 2 * gib, pids: 0 })
    expect(screen.getByText(/Now: 8 CPUs, 2 GiB memory, 4096 processes\./)).toBeTruthy()
    expect((screen.getByLabelText('Memory (GiB)') as HTMLInputElement).value).toBe('2')
  })

  test('refuses a limit that is not a positive number', async () => {
    seed()
    const client = { serverRunLimits: vi.fn(async () => initial), serverInfo: vi.fn(async () => serverInfo) } as unknown as Api
    render(<ServerSection client={client} />)
    await act(async () => {})

    fireEvent.change(screen.getByLabelText('Processes'), { target: { value: '1.5' } })
    expect(screen.getByRole('alert').textContent).toBe('Processes must be a whole number above 0.')
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
  })
})
