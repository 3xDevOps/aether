import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { SysboxNudge } from '@/components/sysbox-nudge'
import type { Api } from '@/lib/api'
import type { GatewayCapabilities, SysboxStatus } from '@/lib/types'
import { ServerSection } from '@/routes/settings/server'
import { useStore } from '@/store'
import { alice, bob, serverInfo } from '@/test/fixtures'

const withMethod: GatewayCapabilities = {
  gateway: 'remote',
  methods: ['server.info', 'server.sysbox.status', 'server.sysbox.install'],
  ws: [],
}

function seed(member = alice, capabilities: GatewayCapabilities | null = withMethod) {
  useStore.setState({
    info: { ...serverInfo, member },
    connection: 'live',
    hydrated: true,
    capabilities,
    sysboxNudgeDismissed: false,
  })
}

function client(statuses: SysboxStatus[], install: SysboxStatus = { state: 'installing' }) {
  const rest = [...statuses]
  return {
    sysboxStatus: vi.fn(async () => (rest.length > 1 ? rest.shift()! : rest[0]!)),
    sysboxInstall: vi.fn(async () => install),
    serverInfo: vi.fn(async () => serverInfo),
  } as unknown as Api & { sysboxStatus: ReturnType<typeof vi.fn>; sysboxInstall: ReturnType<typeof vi.fn> }
}

async function settle() {
  await act(async () => {})
}

async function tick(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

describe('Docker inside runs row', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  test('on', async () => {
    seed()
    render(<ServerSection client={client([{ state: 'on' }])} />)
    await settle()
    expect(screen.getByText('Docker inside runs')).toBeTruthy()
    expect(screen.getByText('On')).toBeTruthy()
    expect(screen.getByText('Agents can run docker, compose and buildx in their runs.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
  })

  test('off offers Install', async () => {
    seed()
    render(<ServerSection client={client([{ state: 'off' }])} />)
    await settle()
    expect(screen.getByText('Off')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Install' })).toBeTruthy()
  })

  test('unsupported shows the reason verbatim', async () => {
    seed()
    render(<ServerSection client={client([{ state: 'unsupported', reason: 'kernel 4.9 is too old' }])} />)
    await settle()
    expect(screen.getByText('Unavailable on this host')).toBeTruthy()
    expect(screen.getByText('kernel 4.9 is too old')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
  })

  test('off with a reason shows it verbatim', async () => {
    seed()
    render(<ServerSection client={client([{ state: 'off', reason: 'Docker is not running' }])} />)
    await settle()
    expect(screen.getByText('Docker is not running')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Install' })).toBeTruthy()
  })

  test('off with an empty reason keeps the explanation', async () => {
    seed()
    render(<ServerSection client={client([{ state: 'off', reason: '' }])} />)
    await settle()
    expect(screen.getByText(/isolated from this server/)).toBeTruthy()
  })

  test('a lost install response re-reads status and polls', async () => {
    seed()
    const api = client([{ state: 'off' }, { state: 'installing' }, { state: 'on' }])
    api.sysboxInstall.mockRejectedValueOnce(new Error('network down'))
    render(<ServerSection client={api} />)
    await settle()
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    await settle()
    expect(api.sysboxInstall).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/Installing…/)).toBeTruthy()
    expect(screen.queryByText('network down')).toBeNull()
    await tick(2000)
    expect(screen.getByText('On')).toBeTruthy()
  })

  test('an install error shows only when the status read also fails', async () => {
    seed()
    const api = client([{ state: 'off' }])
    api.sysboxInstall.mockRejectedValueOnce(new Error('network down'))
    api.sysboxStatus.mockResolvedValueOnce({ state: 'off' }).mockRejectedValueOnce(new Error('still down'))
    render(<ServerSection client={api} />)
    await settle()
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    await settle()
    expect(screen.getByText('network down')).toBeTruthy()
  })

  test('the not-root reason hides Install and offers the command', async () => {
    seed()
    const reason = 'aether-server is not root; run: sudo aether-server sysbox install'
    render(<ServerSection client={client([{ state: 'off', reason }])} />)
    await settle()
    expect(screen.getByText(reason)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Copy sudo aether-server sysbox install' })).toBeTruthy()
  })

  test('install, poll, then on announces the change', async () => {
    seed()
    const api = client([{ state: 'off' }, { state: 'installing' }, { state: 'installing' }, { state: 'on' }])
    render(<ServerSection client={api} />)
    await settle()
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    await settle()
    expect(api.sysboxInstall).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/Installing… this takes a minute or two/)).toBeTruthy()
    await tick(2000)
    expect(screen.getByText(/Installing…/)).toBeTruthy()
    await tick(2000)
    await tick(2000)
    expect(screen.getByText('On')).toBeTruthy()
    expect(screen.getByRole('status').textContent).toBe('New runs get it now. Reopen your Environment to use it there.')
    expect(screen.queryByText(/Installing…/)).toBeNull()
  })

  test('failed shows the reason, the log and Try again', async () => {
    seed()
    const api = client([{ state: 'failed', reason: 'apt exited 100', log: 'E: unable to locate package' }])
    render(<ServerSection client={api} />)
    await settle()
    expect(screen.getByText('Install failed')).toBeTruthy()
    expect(screen.getByText('apt exited 100')).toBeTruthy()
    expect(screen.getByText('Show output')).toBeTruthy()
    expect(screen.getByText('E: unable to locate package')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await settle()
    expect(api.sysboxInstall).toHaveBeenCalledTimes(1)
  })

  test('non-admins see nothing and make no call', async () => {
    seed(bob)
    const api = client([{ state: 'off' }])
    render(<ServerSection client={api} />)
    await settle()
    expect(screen.queryByText('Docker inside runs')).toBeNull()
    expect(api.sysboxStatus).not.toHaveBeenCalled()
  })

  test('an older server without the method shows nothing', async () => {
    seed(alice, { gateway: 'remote', methods: ['server.info'], ws: [] })
    const api = client([{ state: 'off' }])
    render(<ServerSection client={api} />)
    await settle()
    expect(screen.queryByText('Docker inside runs')).toBeNull()
    expect(api.sysboxStatus).not.toHaveBeenCalled()
  })
})

describe('Docker nudge', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    window.localStorage.clear()
  })

  test('shows when off and installs from the callout', async () => {
    seed()
    const api = client([{ state: 'off' }])
    render(<SysboxNudge client={api} />)
    await settle()
    expect(screen.getByText('Let agents use Docker in their runs')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    await settle()
    expect(api.sysboxInstall).toHaveBeenCalledTimes(1)
  })

  test('shows unsupported after an install from off, then Dismiss', async () => {
    seed()
    const api = client([{ state: 'off' }, { state: 'unsupported', reason: 'cannot query Docker' }], { state: 'unsupported', reason: 'cannot query Docker' })
    render(<SysboxNudge client={api} />)
    await settle()
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    await settle()
    expect(screen.getByText('cannot query Docker')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('cannot query Docker')).toBeNull()
  })

  test('off with an empty reason keeps the explanation', async () => {
    seed()
    render(<SysboxNudge client={client([{ state: 'off', reason: '' }])} />)
    await settle()
    expect(screen.getByText(/isolated from this server/)).toBeTruthy()
  })

  test('shows the off reason and the not-root command', async () => {
    seed()
    render(<SysboxNudge client={client([{ state: 'off', reason: 'run: sudo aether-server sysbox install' }])} />)
    await settle()
    expect(screen.getByText('run: sudo aether-server sysbox install')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Copy sudo aether-server sysbox install' })).toBeTruthy()
  })

  test('stays hidden when on, unsupported, for non-admins and older servers', async () => {
    for (const [member, caps, state] of [
      [alice, withMethod, 'on'],
      [alice, withMethod, 'unsupported'],
      [bob, withMethod, 'off'],
      [alice, { gateway: 'remote', methods: ['server.info'], ws: [] } as GatewayCapabilities, 'off'],
    ] as const) {
      seed(member, caps)
      const { container } = render(<SysboxNudge client={client([{ state }])} />)
      await settle()
      expect(container.textContent).toBe('')
      cleanup()
    }
  })

  test('dismissal persists and hides it', async () => {
    seed()
    render(<SysboxNudge client={client([{ state: 'off' }])} />)
    await settle()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('Let agents use Docker in their runs')).toBeNull()
    const stored = JSON.parse(window.localStorage.getItem('aether.ui') ?? '{}') as { state?: Record<string, unknown> }
    expect(stored.state?.sysboxNudgeDismissed).toBe(true)
  })
})
