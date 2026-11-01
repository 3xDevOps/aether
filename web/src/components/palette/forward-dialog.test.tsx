import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { ForwardDialog } from '@/components/palette/forward-dialog'
import { runCommands } from '@/lib/commands'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const forwardMocks = vi.hoisted(() => ({
  localForwardStart: vi.fn(),
  localForwardStop: vi.fn(),
  localForwardStatus: vi.fn(),
  devPortList: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: forwardMocks }))

beforeEach(() => {
  vi.clearAllMocks()
  forwardMocks.localForwardStatus.mockResolvedValue({
    forwards: [
      { target: 'run:run_1', port: 1455, local_port: 1455, conns: 1 },
      { target: 'run:run_2', port: 3000, local_port: 3000, conns: 0 },
    ],
  })
  forwardMocks.localForwardStart.mockResolvedValue({
    target: 'run:run_1',
    port: 8000,
    local_port: 8000,
    state: 'active',
  })
  forwardMocks.localForwardStop.mockResolvedValue({
    target: 'run:run_1',
    port: 1455,
    state: 'stopped',
  })
  forwardMocks.devPortList.mockResolvedValue({
    ports: [
      { port: 5173, address: '127.0.0.1', process: 'node' },
      { port: 8000, address: '0.0.0.0', process: 'python3.12' },
    ],
  })
  useStore.setState({
    capabilities: { gateway: 'local', methods: ['*'], ws: [], local: ['forward.start'] },
    paletteDialog: 'forward',
    paletteRunID: null,
    paletteForwardTarget: 'run:run_1',
  })
})

function forward(port: string) {
  fireEvent.change(screen.getByLabelText('Port'), { target: { value: port } })
  fireEvent.click(screen.getByRole('button', { name: 'Forward' }))
}

describe('forward dialog', () => {
  it('forwards a port of the focused run and links what is forwarded', async () => {
    render(<ForwardDialog />)

    const link = await screen.findByRole('link', { name: 'localhost:1455' })
    expect(link.getAttribute('href')).toBe('http://localhost:1455')
    expect(screen.getByText('1 connection')).toBeDefined()
    expect(screen.queryByRole('link', { name: 'localhost:3000' })).toBeNull()
    expect((screen.getByLabelText('Port') as HTMLInputElement).value).toBe('')

    forward('8000')
    await waitFor(() =>
      expect(api.localForwardStart).toHaveBeenCalledWith('run:run_1', 8000),
    )
    expect(api.devPortList).toHaveBeenCalledWith({ run_id: 'run_1' })
    expect(screen.queryByRole('status')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    await waitFor(() =>
      expect(api.localForwardStop).toHaveBeenCalledWith('run:run_1', 1455),
    )
  })

  it('refuses a port only the run can reach and names the address it is bound to', async () => {
    render(<ForwardDialog />)

    forward('5173')

    expect((await screen.findByRole('alert')).textContent).toBe('Forward failed: Port 5173 is bound to 127.0.0.1 inside the session, so only the session can reach it. Start the server on 0.0.0.0 to open it on this computer.')
    expect(api.localForwardStart).not.toHaveBeenCalled()
  })

  it('forwards a port nothing listens on yet and says so', async () => {
    render(<ForwardDialog />)

    forward('3000')

    expect((await screen.findByRole('status')).textContent).toBe('Nothing in the session is listening on port 3000 yet. Its link works once something does.')
    expect(api.localForwardStart).toHaveBeenCalledWith('run:run_1', 3000)
  })

  it('keeps the gateway’s refusal', async () => {
    forwardMocks.localForwardStart.mockRejectedValue(new Error('localops: listen on 127.0.0.1:8000: bind: address already in use'))
    render(<ForwardDialog />)

    forward('8000')

    expect((await screen.findByRole('alert')).textContent).toBe('Forward failed: listen on 127.0.0.1:8000: bind: address already in use')
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('forwards from the environment, whose ports no method lists', async () => {
    useStore.setState({ paletteForwardTarget: 'terminal' })
    render(<ForwardDialog />)

    forward('1455')

    await waitFor(() =>
      expect(api.localForwardStart).toHaveBeenCalledWith('terminal', 1455),
    )
    expect(api.devPortList).not.toHaveBeenCalled()
  })
})

describe('forward command', () => {
  const context = (local: boolean, status: 'running' | 'failed') => ({
    run: toRecord(run({ status })),
    paused: false,
    cap: {
      hasMethod: () => true,
      hasLocal: (verb: string) => local && verb === 'forward.start',
      hasWS: () => true,
    },
    members: {},
    self: { id: 'mem_alice' as string, role: 'collaborator' as const },
  })

  it('requires the local gateway and an unfinished run', () => {
    expect(runCommands(context(false, 'running')).some((command) => command.id === 'forward')).toBe(false)
    expect(runCommands(context(true, 'running')).some((command) => command.id === 'forward')).toBe(true)
    expect(runCommands(context(true, 'failed')).some((command) => command.id === 'forward')).toBe(false)
  })
})
