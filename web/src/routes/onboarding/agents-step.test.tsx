import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import type { Api } from '@/lib/api'
import type { ConfigImportResult, ConfigRoot, GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { FirstRunStep } from '@/routes/onboarding/steps'
import { useStore } from '@/store'
import { capability } from '@/store/hooks'
import {
  alice,
  bob,
  fakeApi,
  serverInfo,
  workspace,
} from '@/test/fixtures'
import {
  MAX_IMPORT_FILE_BYTES,
  MAX_IMPORT_FILES,
  MAX_IMPORT_TOTAL_BYTES,
  ProfileImport,
  prepareDirectoryImport,
} from '@/components/profile-import'

const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: ['link.status', 'link.repo', 'env.harnesses'],
}

function seed(caps: GatewayCapabilities = localCaps) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    identityKey: `${window.location.origin}\u0000${serverInfo.tailnet_hostname ?? ''}\u0000${alice.id}`,
    configImportPending: false,
    capabilities: caps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'onboarding', params: {} },
    onboardingStep: 'Link',
    onboardingFurthest: 'Link',
    onboardingWorkspace: '',
    onboardingRepo: null,
    onboardingFirstRun: { harness: '', task: '' },
  })
}

function renderStep(client: Api, caps: GatewayCapabilities = localCaps) {
  seed(caps)
  const onNext = vi.fn()
  const onReady = vi.fn()
  function Host() {
    const [setup, onSetup] = useState('')
    return (
      <AgentsStep
        client={client}
        caps={capability(caps)}
        workspace={workspace}
        setup={setup}
        onSetup={onSetup}
        onNext={onNext}
        onReady={onReady}
      />
    )
  }
  const view = render(<Host />)
  return { onNext, onReady, view }
}

function directoryFile(
  path: string,
  content: string | Uint8Array,
  root = '.claude',
): File {
  const fileContent = typeof content === 'string' ? content : new Uint8Array(content).buffer
  const file = new File([fileContent], path.slice(path.lastIndexOf('/') + 1))
  Object.defineProperty(file, 'webkitRelativePath', {
    configurable: true,
    value: `${root}/${path}`,
  })
  return file
}
function policyRoot(overrides: Partial<ConfigRoot> = {}): ConfigRoot {
  return {
    harness: 'claude',
    path: '~/.claude',
    runtime_ignores: [],
    ...overrides,
  }
}


async function choose(files: File[]) {
  const input = screen.getByLabelText('Choose configuration directory')
  await act(async () => {
    fireEvent.change(input, { target: { files } })
  })
  await waitFor(() => {
    expect(
      screen.queryByText('Preview') ||
        screen.queryByLabelText('Configuration destination'),
    ).toBeTruthy()
  })
}

async function confirmImport() {
  const button = screen.getByRole('button', { name: 'Import configuration' })
  await waitFor(() => expect(button).toHaveProperty('disabled', false))
  fireEvent.click(button)
}

beforeEach(() => {
  vi.restoreAllMocks()
  seed()
})

describe('agents step', () => {
  it('keeps setup and import independent of local gateway capabilities', async () => {
    const client = fakeApi()
    renderStep(client, {
      gateway: 'remote',
      methods: ['*'],
      ws: ['events', 'attach'],
      local: ['link.status'],
    })

    expect(await screen.findByText('Claude Code')).toBeDefined()
    expect(screen.getByRole('region', { name: 'Bring your configuration' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Choose directory' })).toBeDefined()
  })


  it('drops a stale directory read when the source changes', async () => {
    const client = fakeApi()
    renderStep(client)
    const pending = Promise.withResolvers<ArrayBuffer>()
    const readOld = vi.fn(() => pending.promise)
    const oldFile = directoryFile('old.md', 'old')
    Object.defineProperty(oldFile, 'arrayBuffer', {
      configurable: true,
      value: readOld,
    })
    const input = screen.getByLabelText('Choose configuration directory')
    fireEvent.change(input, { target: { files: [oldFile] } })
    await waitFor(() => expect(readOld).toHaveBeenCalledOnce())

    fireEvent.change(input, {
      target: { files: [directoryFile('new.md', 'new')] },
    })
    await waitFor(() => expect(screen.getByText('Preview')).toBeDefined())
    await act(async () => pending.resolve(new TextEncoder().encode('old').buffer))
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledTimes(1))
    expect(client.configImport).toHaveBeenCalledWith({
      harness: 'claude',
      files: [{ path: 'new.md', content_base64: 'bmV3', mode: 0o644 }],
    })
  })

  it('requires a destination when the selected basename is ambiguous', async () => {
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [
          policyRoot({ harness: 'claude', path: '~/.shared' }),
          policyRoot({ harness: 'codex', path: '~/.shared' }),
        ],
      })),
    })
    renderStep(client)
    const file = directoryFile('settings.json', '{}')
    Object.defineProperty(file, 'webkitRelativePath', {
      configurable: true,
      value: 'shared/settings.json',
    })
    const read = vi.fn(async () => new TextEncoder().encode('{}').buffer)
    Object.defineProperty(file, 'arrayBuffer', {
      configurable: true,
      value: read,
    })
    await choose([file])
    expect(read).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()

    const destination = screen.getByLabelText('Configuration destination')
    fireEvent.change(destination, { target: { value: 'codex' } })
    await screen.findByText('Preview')
    expect(read).toHaveBeenCalledTimes(1)
    expect((screen.getByRole('button', { name: 'Import configuration' }) as HTMLButtonElement).disabled).toBe(false)
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledTimes(1))
    expect(client.configImport).toHaveBeenCalledWith({
      harness: 'codex',
      files: [{ path: 'settings.json', content_base64: 'e30=', mode: 0o644 }],
    })
  })

  it('reports server exclusions and supports another explicit import after success', async () => {
    const client = fakeApi({
      configImport: vi.fn(async ({ harness, files }) => ({
        harness,
        files: files.length,
        bytes: 2,
        excluded: [
          {
            path: 'notes.md',
            reason: 'secret',
            detail: 'secret detected (aws-access-key) at 3:4',
          },
        ],
      })),
    })
    renderStep(client)
    await choose([directoryFile('notes.md', 'ok')])
    await confirmImport()
    await waitFor(() => expect(screen.getByText(/Imported 1 files/)).toBeDefined())
    expect(screen.getByText('notes.md')).toBeDefined()
    expect(screen.getByText(/secret detected \(aws-access-key\) at 3:4/)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    expect(client.configImport).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', false)
    fireEvent.click(screen.getByRole('button', { name: 'Open remote files' }))
    expect(useStore.getState().route.name).toBe('files')
    const picker = screen.getByLabelText('Choose configuration directory')
    const openPicker = vi.spyOn(picker, 'click')
    fireEvent.click(screen.getByRole('button', { name: 'Import another directory' }))
    expect(openPicker).toHaveBeenCalledOnce()
    await choose([directoryFile('updated.json', '{}')])
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledTimes(2))
    expect(client.configImport).toHaveBeenLastCalledWith({
      harness: 'claude',
      files: [{ path: 'updated.json', content_base64: 'e30=', mode: 0o644 }],
    })
  })

  it('shows a partial import as a warning with committed paths and a real error', async () => {
    const error = 'config.import: write settings.json: permission denied'
    const client = fakeApi({
      configImport: vi.fn(async ({ harness }) => ({
        harness,
        files: 1,
        bytes: 2,
        excluded: [{ path: 'blocked.md', reason: 'secret' }],
        error,
        imported_paths: ['settings.json'],
      })),
    })
    renderStep(client)
    await choose([
      directoryFile('settings.json', '{}'),
      directoryFile('blocked.md', 'ok'),
    ])
    await confirmImport()
    const warning = await screen.findByRole('alert')
    expect(warning.textContent).toContain(error)
    expect(screen.getByText(/Import incomplete: 1 files \(2 B\) imported into/)).toBeDefined()
    expect(screen.getByText('settings.json')).toBeDefined()
    expect(screen.getByText(/Copied files remain\./)).toBeDefined()
    expect(screen.queryByText(/Imported 1 files \(2 B\) into/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Inspect Files' }))
    expect(useStore.getState().route.name).toBe('files')
  })

  it('marks a rejected import outcome unknown without hiding the error', async () => {
    const error = 'config.import: connection reset by peer'
    const client = fakeApi({
      configImport: vi.fn(async () => {
        throw new Error(error)
      }),
    })
    renderStep(client)
    await choose([directoryFile('settings.json', '{}')])
    await confirmImport()
    const warning = await screen.findByRole('alert')
    expect(warning.textContent).toContain(error)
    expect(warning.textContent).toMatch(/unknown/i)
    fireEvent.click(screen.getByRole('button', { name: 'Inspect Files' }))
    expect(useStore.getState().route.name).toBe('files')
  })


  it('keeps an outstanding import exclusive across remounts', async () => {
    const pending = Promise.withResolvers<ConfigImportResult>()
    const client = fakeApi({ configImport: vi.fn(() => pending.promise) })
    const first = render(<ProfileImport client={client} />)
    await choose([directoryFile('settings.json', '{}')])
    await confirmImport()
    first.unmount()
    render(<ProfileImport client={client} />)

    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('status').textContent).toContain('Importing')
    pending.resolve({ harness: 'claude', files: 1, bytes: 2, excluded: [] })
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', false)
    })
  })

  it('requires a new directory review and consent for a different identity, but not a reconnect', async () => {
    const nextRoots = Promise.withResolvers<{ roots: ConfigRoot[] }>()
    const client = fakeApi({
      configRoots: vi.fn()
        .mockResolvedValueOnce({ roots: [policyRoot()] })
        .mockImplementationOnce(() => nextRoots.promise),
    })
    render(<ProfileImport client={client} />)
    await choose([
      directoryFile('settings.json', '{}'),
      directoryFile('large.bin', new Uint8Array(MAX_IMPORT_FILE_BYTES + 1)),
    ])
    fireEvent.click(screen.getByRole('checkbox', { name: /I understand this selection is incomplete/ }))
    expect(screen.getByRole('button', { name: 'Import configuration' })).toHaveProperty('disabled', false)

    const identity = useStore.getState().identityKey!
    act(() => useStore.getState().reconnect())
    act(() => {
      useStore.getState().setIdentityKey(identity)
      useStore.getState().setInfo({ ...serverInfo })
      useStore.getState().setConnection('live')
      useStore.getState().setHydrated(true)
    })
    expect(screen.getByText('Preview')).toBeDefined()
    expect(screen.getByRole('checkbox')).toHaveProperty('checked', true)
    expect(screen.getByRole('button', { name: 'Import configuration' })).toHaveProperty('disabled', false)
    expect(client.configRoots).toHaveBeenCalledTimes(1)
    expect(client.configImport).not.toHaveBeenCalled()

    act(() => {
      useStore.getState().setIdentityKey(`${window.location.origin}\u0000${serverInfo.tailnet_hostname ?? ''}\u0000${bob.id}`)
      useStore.getState().setInfo({ ...serverInfo, member: bob })
    })
    expect(screen.queryByText('Preview')).toBeNull()
    expect(screen.queryByText('settings.json')).toBeNull()
    expect(screen.queryByText('large.bin')).toBeNull()
    expect(screen.queryByText(/Selected directory:/)).toBeNull()
    expect(screen.queryByLabelText('Configuration destination')).toBeNull()
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    expect(client.configRoots).toHaveBeenCalledTimes(2)
    expect(client.configImport).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText('Choose configuration directory'), {
      target: {
        files: [
          directoryFile('settings.json', '{"new":true}', '.codex'),
          directoryFile('large.bin', new Uint8Array(MAX_IMPORT_FILE_BYTES + 1), '.codex'),
        ],
      },
    })
    expect(screen.queryByText('Preview')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    await act(async () => nextRoots.resolve({
      roots: [policyRoot({ harness: 'codex', path: '~/.codex' })],
    }))
    await screen.findByText('Preview')
    expect(screen.getByRole('checkbox')).toHaveProperty('checked', false)
    const submit = screen.getByRole('button', { name: 'Import configuration' })
    expect(submit).toHaveProperty('disabled', true)
    fireEvent.click(submit)
    expect(client.configImport).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('checkbox'))
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledOnce())
    expect(client.configImport).toHaveBeenCalledWith({
      harness: 'codex',
      files: [{ path: 'settings.json', content_base64: 'eyJuZXciOnRydWV9', mode: 0o644 }],
    })
  })

  it('keeps the prior identity import pending without showing its result to the new identity', async () => {
    const pending = Promise.withResolvers<ConfigImportResult>()
    const client = fakeApi({
      configRoots: vi.fn()
        .mockResolvedValueOnce({ roots: [policyRoot()] })
        .mockResolvedValueOnce({ roots: [policyRoot({ harness: 'codex', path: '~/.codex' })] }),
      configImport: vi.fn()
        .mockImplementationOnce(() => pending.promise)
        .mockResolvedValueOnce({ harness: 'codex', files: 1, bytes: 3, excluded: [] }),
    })
    render(<ProfileImport client={client} />)
    await choose([directoryFile('old-settings.json', '{}')])
    await confirmImport()
    expect(client.configImport).toHaveBeenCalledOnce()

    act(() => {
      useStore.getState().setIdentityKey(`${window.location.origin}\u0000${serverInfo.tailnet_hostname ?? ''}\u0000${bob.id}`)
      useStore.getState().setInfo({ ...serverInfo, member: bob })
    })
    expect(useStore.getState().configImportPending).toBe(true)
    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', true)
    expect(screen.getByLabelText('Choose configuration directory')).toHaveProperty('disabled', true)
    expect(screen.queryByText('Preview')).toBeNull()
    expect(screen.queryByText('old-settings.json')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    await waitFor(() => expect(client.configRoots).toHaveBeenCalledTimes(2))
    fireEvent.change(screen.getByLabelText('Choose configuration directory'), {
      target: { files: [directoryFile('blocked.json', '{}', '.codex')] },
    })
    expect(screen.queryByText(/Selected directory:/)).toBeNull()
    expect(client.configImport).toHaveBeenCalledOnce()
    expect(useStore.getState().configImportPending).toBe(true)

    await act(async () => pending.resolve({
      harness: 'claude', files: 1, bytes: 2, excluded: [],
    }))
    expect(useStore.getState().configImportPending).toBe(false)
    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', false)
    expect(screen.queryByText(/Imported 1 files/)).toBeNull()
    expect(screen.queryByText('Preview')).toBeNull()
    expect(screen.queryByText(/Selected directory:/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    expect(client.configImport).toHaveBeenCalledOnce()

    await choose([directoryFile('new-settings.json', 'new', '.codex')])
    expect(client.configImport).toHaveBeenCalledOnce()
    await confirmImport()
    await screen.findByText(/Imported 1 files \(3 B\) into/)
    expect(client.configImport).toHaveBeenCalledTimes(2)
    expect(client.configImport).toHaveBeenLastCalledWith({
      harness: 'codex',
      files: [{ path: 'new-settings.json', content_base64: 'bmV3', mode: 0o644 }],
    })
  })

  it('leaves the optional skip action available with an import result or error', async () => {
    const client = fakeApi()
    const { onNext } = renderStep(client)
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalledTimes(1)
  })

  it('requires consent when OMP dependencies exhaust the file cap before settings', async () => {
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [policyRoot({ harness: 'omp', path: '~/.omp', runtime_ignores: ['agent/cache/'] })],
      })),
    })
    render(<ProfileImport client={client} />)
    const dependencies = Array.from({ length: MAX_IMPORT_FILES + 55 }, (_, index) => {
      const file = directoryFile(`agent/npm/node_modules/package/${index}.js`, '', '.omp')
      Object.defineProperty(file, 'arrayBuffer', { value: async () => new ArrayBuffer(0) })
      return file
    })
    await choose([...dependencies, directoryFile('agent/settings.json', '{}', '.omp')])
    expect(screen.getByRole('alert').textContent).toContain('56 files omitted')
    expect(screen.getByRole('alert').textContent).toMatch(/Settings or dependencies may be absent/)
    const accepted = screen.getByText(`Accepted paths: ${MAX_IMPORT_FILES}`)
    fireEvent.click(accepted)
    expect(screen.getByText(`agent/npm/node_modules/package/${MAX_IMPORT_FILES - 1}.js`)).toBeDefined()
    // Settings is beyond the old fifty-row exclusion cutoff.
    expect(screen.getByText('agent/settings.json')).toBeDefined()
    const submit = screen.getByRole('button', { name: 'Import configuration' })
    expect(submit).toHaveProperty('disabled', true)
    fireEvent.click(submit)
    expect(client.configImport).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('checkbox', { name: /I understand this selection is incomplete/ }))
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledOnce())
    const sent = vi.mocked(client.configImport).mock.calls[0][0]
    expect(sent.harness).toBe('omp')
    expect(sent.files.map(({ path }) => path)).toEqual(
      dependencies.slice(0, MAX_IMPORT_FILES).map((file) => file.webkitRelativePath.slice('.omp/'.length)),
    )
  })

  it.each(['over-budget', 'too-large'])('requires consent for %s omissions before sending accepted files', async (reason) => {
    const client = fakeApi()
    render(<ProfileImport client={client} />)
    const bytes = new ArrayBuffer(MAX_IMPORT_FILE_BYTES)
    const count = reason === 'over-budget' ? MAX_IMPORT_TOTAL_BYTES / MAX_IMPORT_FILE_BYTES : 1
    const files = Array.from({ length: count }, (_, index) => {
      const file = directoryFile(`part-${index}.bin`, '')
      Object.defineProperty(file, 'size', { value: bytes.byteLength })
      Object.defineProperty(file, 'arrayBuffer', { value: async () => bytes })
      return file
    })
    await choose([
      ...files,
      directoryFile('omitted.bin', reason === 'too-large' ? new Uint8Array(MAX_IMPORT_FILE_BYTES + 1) : 'over budget'),
    ])
    expect(screen.getByRole('alert').textContent).toContain('1 files omitted')
    expect(screen.getByText(new RegExp(`${reason}:`))).toBeDefined()
    const submit = screen.getByRole('button', { name: 'Import configuration' })
    expect(submit).toHaveProperty('disabled', true)
    fireEvent.click(submit)
    expect(client.configImport).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('checkbox', { name: /I understand this selection is incomplete/ }))
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledOnce())
    expect(vi.mocked(client.configImport).mock.calls[0][0].files.map(({ path }) => path))
      .toEqual(files.map((file) => file.name))
  })

  it('does not require incomplete-selection consent for expected credential and runtime exclusions', async () => {
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [policyRoot({ runtime_ignores: ['sessions/'] })],
      })),
    })
    render(<ProfileImport client={client} />)
    await choose([
      directoryFile('settings.json', '{}'),
      directoryFile('auth.json', 'private'),
      directoryFile('sessions/latest.json', 'history'),
    ])
    expect(screen.queryByRole('checkbox')).toBeNull()
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledWith({
      harness: 'claude',
      files: [{ path: 'settings.json', content_base64: 'e30=', mode: 0o644 }],
    }))
  })

  it('invalidates consent when either destination or source changes', async () => {
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [policyRoot(), policyRoot({ harness: 'codex', path: '~/.codex' })],
      })),
    })
    render(<ProfileImport client={client} />)
    const files = [
      directoryFile('settings.json', '{}', 'custom'),
      directoryFile('large.bin', new Uint8Array(MAX_IMPORT_FILE_BYTES + 1), 'custom'),
    ]
    await choose(files)
    fireEvent.change(screen.getByLabelText('Configuration destination'), { target: { value: 'claude' } })
    await screen.findByText('Preview')
    fireEvent.click(screen.getByRole('checkbox'))
    expect(screen.getByRole('button', { name: 'Import configuration' })).toHaveProperty('disabled', false)

    fireEvent.change(screen.getByLabelText('Configuration destination'), { target: { value: 'codex' } })
    await screen.findByText('Preview')
    expect(screen.getByRole('checkbox')).toHaveProperty('checked', false)
    expect(screen.getByRole('button', { name: 'Import configuration' })).toHaveProperty('disabled', true)
    fireEvent.click(screen.getByRole('checkbox'))

    await choose(files)
    fireEvent.change(screen.getByLabelText('Configuration destination'), { target: { value: 'codex' } })
    await screen.findByText('Preview')
    expect(screen.getByRole('checkbox')).toHaveProperty('checked', false)
    const submit = screen.getByRole('button', { name: 'Import configuration' })
    expect(submit).toHaveProperty('disabled', true)
    fireEvent.click(submit)
    expect(client.configImport).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('checkbox'))
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledOnce())
  })
})

describe('directory import bounds', () => {
  it('filters credentials, runtime history and oversized files before reading', async () => {
    const root = 'renamed-agent-home'
    const policy = policyRoot({
      harness: 'omp',
      path: `~/${root}`,
      runtime_ignores: ['agent/cache/', 'logs/', 'run/'],
    })
    const read = vi.fn(async () => new ArrayBuffer(0))
    const unreadable = (path: string, content: string | Uint8Array) => {
      const file = directoryFile(path, content, root)
      Object.defineProperty(file, 'arrayBuffer', {
        configurable: true,
        value: read,
      })
      return file
    }
    const runtime = [
      'agent/cache/state.json',
      'logs/omp.log',
      'run/state.json',
    ]
    const oversized = unreadable(
      'big.bin',
      new Uint8Array(MAX_IMPORT_FILE_BYTES + 1),
    )
    const prepared = await prepareDirectoryImport(
      [
        unreadable('.credentials.json', 'secret'),
        unreadable('agent/agent.db', 'secret'),
        unreadable('agent/agent.db-wal', 'secret'),
        unreadable('agent/agent.db-shm', 'secret'),
        ...runtime.map((path) => unreadable(path, 'runtime')),
        oversized,
        directoryFile('agent/skills/work.md', 'skill', root),
        directoryFile('agent/extensions/work.ts', 'extension', root),
        directoryFile('agent/npm/package/index.js', 'npm', root),
        directoryFile('agent/sessions-note.md', 'kept', root),
        directoryFile('cacheable.json', 'kept', root),
        directoryFile('empty.txt', '', root),
      ],
      policy,
    )

    expect(read).not.toHaveBeenCalled()
    expect(prepared?.files.map(({ path }) => path)).toEqual([
      'agent/skills/work.md',
      'agent/extensions/work.ts',
      'agent/npm/package/index.js',
      'agent/sessions-note.md',
      'cacheable.json',
      'empty.txt',
    ])
    expect(prepared?.bytes).toBe(25)
    expect(prepared?.excluded.filter(({ reason }) => reason === 'runtime').map(({ path }) => path)).toEqual(runtime)
    expect(prepared?.excluded.map(({ path, reason }) => ({ path, reason }))).toEqual([
      { path: '.credentials.json', reason: 'credential' },
      { path: 'agent/agent.db', reason: 'credential' },
      { path: 'agent/agent.db-wal', reason: 'credential' },
      { path: 'agent/agent.db-shm', reason: 'credential' },
      ...runtime.map((path) => ({ path, reason: 'runtime' })),
      { path: 'big.bin', reason: 'too-large' },
    ])
    const destinationSensitive = [
      unreadable('cache/state.json', 'cache'),
      unreadable('logs/omp.log', 'logs'),
      unreadable('run/state.json', 'run'),
    ]
    const claude = await prepareDirectoryImport(
      destinationSensitive,
      policyRoot({
        harness: 'claude',
        path: `~/${root}`,
        runtime_ignores: ['agent/sessions/'],
      }),
    )
    expect(claude?.files.map(({ path }) => path)).toEqual([
      'cache/state.json',
      'logs/omp.log',
      'run/state.json',
    ])
  })
})

describe('the harness the step set up', () => {
  it('marks the harness ready after terminal setup is confirmed', async () => {
    const client = fakeApi()
    const { onReady } = renderStep(client)
    fireEvent.click(await screen.findByRole('button', { name: 'Set up Claude Code' }))
    await screen.findByText(/claude.ai\/install.sh/)
    fireEvent.click(screen.getByRole('button', { name: "I've installed and logged in" }))
    expect(await screen.findByText('Agent installed')).toBeDefined()
    expect(onReady).toHaveBeenCalledWith('claude')
  })

  it('still exposes static terminal instructions when the terminal socket is absent', async () => {
    const client = fakeApi()
    renderStep(client, { ...localCaps, ws: ['events', 'attach'] })
    fireEvent.click(await screen.findByRole('button', { name: 'Set up Claude Code' }))
    await screen.findByText('aether terminal')
    expect(screen.getByText(/claude.ai\/install.sh/)).toBeDefined()
  })
})

describe('first run', () => {
  it('can return to the agents step without an installed agent', async () => {
    seed()
    const onBackToAgents = vi.fn()
    render(
      <FirstRunStep
        client={fakeApi({ agentList: vi.fn(async () => []) })}
        workspace={workspace}
        back={null}
        defaultHarness=""
        onBackToWorkspace={vi.fn()}
        onBackToAgents={onBackToAgents}
      />,
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Set up an agent' }))
    expect(onBackToAgents).toHaveBeenCalledTimes(1)
  })
})


describe('onboarding route', () => {
  it('reaches the agents step while keeping import optional', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Skip' }))
    fireEvent.click(await screen.findByRole('button', { name: `Use ${workspace.name}` }))
    fireEvent.change(await screen.findByLabelText('Repository path'), {
      target: { value: '/home/alice/code/myproject' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    expect(await screen.findByRole('region', { name: 'Bring your configuration' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Skip for now' })).toBeDefined()
  })
})
