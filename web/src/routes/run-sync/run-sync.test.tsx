import { fireEvent, render, screen } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { GatewayCapabilities, SyncSessionStatus } from '@/lib/types'
import '@/routes/run-sync'
import { SyncPanel } from '@/routes/run-sync/sync-panel'
import { Board } from '@/routes/board'
import { useStore, type RootState } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, fakeApi, run, serverInfo, session } from '@/test/fixtures'

// The local gateway's descriptor: the sync verbs the panel rides on.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'shell'],
  local: ['link.status', 'sync.start', 'sync.stop', 'sync.status', 'daemon.status'],
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    sessions: { [session.id]: session },
    members: { [alice.id]: alice },
    runs: {},
    syncSessions: {},
    linkStatus: null,
    acked: {},
    pausedRuns: {},
    inbox: {},
    info: serverInfo,
    capabilities: localCaps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'settings', params: {} },
    ...extra,
  })
}

describe('sync panel', () => {
  it('starts a session, shows it running, and stops it', async () => {
    // sync.status is the only signal /local/v1 offers, so the fake mirrors
    // what the daemon would report after each verb.
    let sessions: SyncSessionStatus[] = []
    const client = fakeApi({
      localSyncStatus: vi.fn(async () => ({ sessions })),
      localSyncStart: vi.fn(async (runID: string) => {
        sessions = [{ run_id: runID, state: 'running', conflict: null }]
        return { run_id: runID, state: 'running' }
      }),
      localSyncStop: vi.fn(async (runID: string) => {
        sessions = []
        return { run_id: runID, state: 'stopped' }
      }),
    })
    seed()
    render(<SyncPanel runID="run_1" client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Start' }))

    expect(await screen.findByText('Overlay running')).toBeDefined()
    expect(client.localSyncStart).toHaveBeenCalledWith('run_1')
    expect(useStore.getState().syncSessions.run_1?.state).toBe('running')

    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))

    expect(await screen.findByText('No sync session for this run.')).toBeDefined()
    expect(client.localSyncStop).toHaveBeenCalledWith('run_1')
  })

  it('shows the refusal verbatim and forces the retry through', async () => {
    const client = fakeApi({
      localSyncStart: vi.fn(async () => {
        throw new ApiError(409, 'sync.start: overlay checkout has local changes')
      }),
    })
    seed()
    render(<SyncPanel runID="run_1" client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Start' }))

    expect(
      await screen.findByText('sync.start: overlay checkout has local changes'),
    ).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Force' }))

    expect(client.localSyncStart).toHaveBeenCalledWith('run_1', true)
  })

  it('renders nothing where the gateway lacks sync.start', () => {
    seed({
      capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    })
    const { container } = render(<SyncPanel runID="run_1" client={fakeApi()} />)

    expect(container.innerHTML).toBe('')
  })

  it('drops a status response that resolves after unmount', async () => {
    // clearInterval stops future ticks but not an in-flight fetch: a late
    // response must not overwrite the store a later mount just filled.
    let resolveStatus!: (v: { sessions: SyncSessionStatus[] }) => void
    const statusPromise = new Promise<{ sessions: SyncSessionStatus[] }>((res) => {
      resolveStatus = res
    })
    const client = fakeApi({
      localSyncStatus: vi.fn(() => statusPromise),
    })
    seed()
    const view = render(<SyncPanel runID="run_1" client={client} />)
    view.unmount()

    resolveStatus({
      sessions: [{ run_id: 'run_1', state: 'running', conflict: null }],
    })
    // Let the resolved fetch's continuation run before asserting.
    await statusPromise
    await Promise.resolve()

    expect(useStore.getState().syncSessions).toEqual({})
  })
})

describe('sync badge', () => {
  it('marks a card only while sync.status reports the run running', () => {
    const syncing = run({ id: 'run_syncing', task: 'mirrored locally' })
    const plain = run({ id: 'run_plain', task: 'no overlay' })
    seed({
      runs: {
        [syncing.id]: toRecord(syncing),
        [plain.id]: toRecord(plain),
      },
      syncSessions: { [syncing.id]: { state: 'running', conflict: null } },
      route: { name: 'board', params: {} },
    })
    render(<Board />)

    const badges = screen.getAllByLabelText('Sync overlay running')
    expect(badges).toHaveLength(1)
    const card = screen
      .getByRole('button', { name: 'mirrored locally' })
      .closest('article')!
    expect(card.contains(badges[0])).toBe(true)
  })
})
