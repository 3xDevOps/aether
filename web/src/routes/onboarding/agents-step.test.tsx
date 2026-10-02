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
  IMPORT_BATCH_BYTES,
  IMPORT_BATCH_FILES,
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

function dependencyFiles(count: number, root = '.claude'): File[] {
  const bytes = new ArrayBuffer(0)
  return Array.from({ length: count }, (_, index) => {
    const file = directoryFile(`agent/npm/node_modules/package/${index}.js`, '', root)
    Object.defineProperty(file, 'arrayBuffer', { value: async () => bytes })
    return file
  })
}

function bufferedFile(path: string, bytes: ArrayBuffer): File {
  const file = directoryFile(path, '')
  Object.defineProperty(file, 'size', { value: bytes.byteLength })
  Object.defineProperty(file, 'arrayBuffer', { configurable: true, value: async () => bytes })
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
  await act(async () => fireEvent.click(button))
}

beforeEach(() => {
  vi.restoreAllMocks()
  useStore.setState(useStore.getInitialState(), true)
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


  it('previews metadata without reading bytes and uploads only the latest selection', async () => {
    const client = fakeApi()
    renderStep(client)
    const oldFile = directoryFile('old.md', 'old')
    const readOld = vi.fn(async () => new TextEncoder().encode('old').buffer)
    Object.defineProperty(oldFile, 'arrayBuffer', { value: readOld })
    const newFile = directoryFile('new.md', 'new')
    const readNew = vi.fn(async () => new TextEncoder().encode('new').buffer)
    Object.defineProperty(newFile, 'arrayBuffer', { value: readNew })

    await choose([oldFile])
    await choose([newFile])
    expect(readOld).not.toHaveBeenCalled()
    expect(readNew).not.toHaveBeenCalled()
    await confirmImport()
    await screen.findByText(/Imported 1 files/)
    expect(readOld).not.toHaveBeenCalled()
    expect(readNew).toHaveBeenCalledOnce()
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
    expect(read).not.toHaveBeenCalled()
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
      configImport: vi.fn(async ({ harness, files }: Parameters<Api['configImport']>[0]) => ({
        harness,
        files: files.filter(({ path }) => path !== 'notes.md').length,
        bytes: 0,
        excluded: files.filter(({ path }) => path === 'notes.md').map(({ path }) => ({
          path,
          reason: 'secret',
          detail: 'secret detected (aws-access-key) at 3:4',
        })),
      })),
    })
    renderStep(client)
    await choose([directoryFile('settings.json', '{}'), directoryFile('notes.md', 'ok')])
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


  it('continues every batch across same-owner navigation and retains the outcome after remount', async () => {
    const pending = Promise.withResolvers<ConfigImportResult>()
    const client = fakeApi({
      configImport: vi.fn()
        .mockImplementationOnce(() => pending.promise)
        .mockResolvedValueOnce({ harness: 'claude', files: 1, bytes: 2, excluded: [] }),
    })
    const first = render(<ProfileImport client={client} />)
    await choose([...dependencyFiles(IMPORT_BATCH_FILES), directoryFile('settings.json', '{}')])
    await confirmImport()
    expect(client.configImport).toHaveBeenCalledOnce()
    act(() => useStore.getState().navigate('files'))
    first.unmount()
    const second = render(<ProfileImport client={client} />)

    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('status').textContent).toMatch(/Importing/)
    await act(async () => pending.resolve({
      harness: 'claude', files: IMPORT_BATCH_FILES, bytes: 0, excluded: [],
    }))
    await screen.findByText(new RegExp(`Imported ${IMPORT_BATCH_FILES + 1} files`))
    expect(client.configImport).toHaveBeenCalledTimes(2)
    expect(client.configImport).toHaveBeenLastCalledWith({
      harness: 'claude',
      files: [{ path: 'settings.json', content_base64: 'e30=', mode: 0o644 }],
    })
    second.unmount()
    render(<ProfileImport client={client} />)
    expect(screen.getByText(new RegExp(`Imported ${IMPORT_BATCH_FILES + 1} files`))).toBeDefined()
    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', false)
    expect(client.configImport).toHaveBeenCalledTimes(2)
  })

  it('requires a new directory review for a different identity, but not a reconnect', async () => {
    const nextRoots = Promise.withResolvers<{ roots: ConfigRoot[] }>()
    const client = fakeApi({
      configRoots: vi.fn()
        .mockResolvedValueOnce({ roots: [policyRoot()] })
        .mockImplementationOnce(() => nextRoots.promise),
    })
    render(<ProfileImport client={client} />)
    await choose([directoryFile('settings.json', '{}')])
    const identity = useStore.getState().identityKey!
    await act(async () => {
      useStore.getState().reconnect()
      useStore.getState().setIdentityKey(identity)
      useStore.getState().setInfo({ ...serverInfo })
      useStore.getState().setConnection('live')
      useStore.getState().setHydrated(true)
    })
    expect(screen.getByText('Preview')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Import configuration' })).toHaveProperty('disabled', false)
    expect(client.configRoots).toHaveBeenCalledTimes(1)

    await act(async () => {
      useStore.getState().setIdentityKey(`${window.location.origin}\u0000${serverInfo.tailnet_hostname ?? ''}\u0000${bob.id}`)
      useStore.getState().setInfo({ ...serverInfo, member: bob })
    })
    expect(screen.queryByText('Preview')).toBeNull()
    expect(screen.queryByText('settings.json')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    expect(client.configImport).not.toHaveBeenCalled()
    await act(async () => {
      fireEvent.change(screen.getByLabelText('Choose configuration directory'), {
        target: { files: [directoryFile('settings.json', '{"new":true}', '.codex')] },
      })
      nextRoots.resolve({ roots: [policyRoot({ harness: 'codex', path: '~/.codex' })] })
    })
    await screen.findByText('Preview')
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledWith({
      harness: 'codex',
      files: [{ path: 'settings.json', content_base64: 'eyJuZXciOnRydWV9', mode: 0o644 }],
    }))
  })

  it.each(['read', 'RPC'] as const)('stops after an identity switch during a deferred %s and hides the old outcome', async (phase) => {
    const read = Promise.withResolvers<ArrayBuffer>()
    const rpc = Promise.withResolvers<ConfigImportResult>()
    const firstFiles = dependencyFiles(IMPORT_BATCH_FILES)
    const nextFile = bufferedFile('old-settings.json', new TextEncoder().encode('{}').buffer)
    const readNext = vi.fn(() => read.promise)
    if (phase === 'read') {
      Object.defineProperty(nextFile, 'arrayBuffer', { value: readNext })
    }
    const client = fakeApi({
      configRoots: vi.fn()
        .mockResolvedValueOnce({ roots: [policyRoot()] })
        .mockResolvedValueOnce({ roots: [policyRoot({ harness: 'codex', path: '~/.codex' })] }),
      configImport: vi.fn()
        .mockImplementationOnce(() => phase === 'RPC' ? rpc.promise : Promise.resolve({
          harness: 'claude', files: firstFiles.length, bytes: 0, excluded: [],
        }))
        .mockResolvedValueOnce({ harness: 'codex', files: 1, bytes: 3, excluded: [] }),
    })
    render(<ProfileImport client={client} />)
    await choose([...firstFiles, nextFile])
    await confirmImport()
    expect(client.configImport).toHaveBeenCalledOnce()
    if (phase === 'read') expect(readNext).toHaveBeenCalledOnce()
    await act(async () => {
      useStore.getState().setIdentityKey(`${window.location.origin}\u0000${serverInfo.tailnet_hostname ?? ''}\u0000${bob.id}`)
      useStore.getState().setInfo({ ...serverInfo, member: bob })
    })
    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', true)
    expect(screen.queryByText('old-settings.json')).toBeNull()
    expect(screen.queryByText('Preview')).toBeNull()
    await act(async () => {
      read.resolve(new TextEncoder().encode('{}').buffer)
      rpc.resolve({ harness: 'claude', files: firstFiles.length, bytes: 0, excluded: [] })
    })
    expect(client.configImport).toHaveBeenCalledOnce()
    expect(screen.queryByText(/Imported \d+ files/)).toBeNull()
    expect(screen.queryByText(/Import incomplete/)).toBeNull()
    expect(screen.queryByText('old-settings.json')).toBeNull()
    expect(screen.getByRole('button', { name: 'Choose directory' })).toHaveProperty('disabled', false)
    await choose([directoryFile('new-settings.json', 'new', '.codex')])
    await confirmImport()
    await screen.findByText(/Imported 1 files \(3 B\) into/)
    expect(client.configImport).toHaveBeenCalledTimes(2)
    expect(client.configImport).toHaveBeenLastCalledWith({
      harness: 'codex',
      files: [{ path: 'new-settings.json', content_base64: 'bmV3', mode: 0o644 }],
    })
  })

  it('leaves configuration import optional when skipping agent setup', async () => {
    const client = fakeApi()
    const { onNext } = renderStep(client)
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalledTimes(1)
  })

  it('imports settings after more than 2000 dependency files in sequential bounded requests', async () => {
    const first = Promise.withResolvers<ConfigImportResult>()
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [policyRoot({ harness: 'omp', path: '~/.omp', runtime_ignores: ['agent/cache/'] })],
      })),
      configImport: vi.fn()
        .mockImplementationOnce(() => first.promise)
        .mockImplementation(async ({ harness, files }) => ({
          harness, files: files.length, bytes: 2, excluded: [],
        })),
    })
    render(<ProfileImport client={client} />)
    const dependencies = dependencyFiles(IMPORT_BATCH_FILES + 55, '.omp')
    const settings = directoryFile('agent/settings.json', '{}', '.omp')
    const readSettings = vi.fn(async () => new TextEncoder().encode('{}').buffer)
    Object.defineProperty(settings, 'arrayBuffer', { value: readSettings })
    await choose([...dependencies, settings])
    await confirmImport()
    expect(client.configImport).toHaveBeenCalledOnce()
    expect(readSettings).not.toHaveBeenCalled()
    await act(async () => first.resolve({
      harness: 'omp', files: IMPORT_BATCH_FILES, bytes: 0, excluded: [],
    }))
    await screen.findByText(new RegExp(`Imported ${dependencies.length + 1} files`))
    const calls = vi.mocked(client.configImport).mock.calls.map(([request]) => request)
    expect(calls).toHaveLength(2)
    expect(calls.every(({ harness, files }) => harness === 'omp' && files.length <= IMPORT_BATCH_FILES)).toBe(true)
    expect(calls.flatMap(({ files }) => files.map(({ path }) => path))).toEqual([
      ...dependencies.map((file) => file.webkitRelativePath.slice('.omp/'.length)),
      'agent/settings.json',
    ])
    expect(calls[1].files.at(-1)).toEqual({
      path: 'agent/settings.json', content_base64: 'e30=', mode: 0o644,
    })
  })

  it('imports more than 20 MiB, including files above 1 MiB, without exceeding a batch byte budget', async () => {
    const bytes = new Uint8Array(6 * 1024 * 1024).fill(0xa5).buffer
    const files = Array.from({ length: 4 }, (_, index) => bufferedFile(`part-${index}.bin`, bytes))
    const client = fakeApi({
      configImport: vi.fn(async ({ harness, files: sent }) => ({
        harness, files: sent.length, bytes: sent.length * bytes.byteLength, excluded: [],
      })),
    })
    render(<ProfileImport client={client} />)
    await choose(files)
    await confirmImport()
    await screen.findByText(/Imported 4 files/)
    const calls = vi.mocked(client.configImport).mock.calls.map(([request]) => request.files)
    expect(calls).toHaveLength(2)
    expect(calls.flat().map(({ path }) => path)).toEqual(files.map(({ name }) => name))
    for (const batch of calls) {
      const decoded = batch.map(({ content_base64 }) => Buffer.from(content_base64, 'base64'))
      expect(decoded.reduce((total, content) => total + content.length, 0)).toBeLessThanOrEqual(IMPORT_BATCH_BYTES)
      for (const content of decoded) {
        expect(content.equals(Buffer.from(bytes))).toBe(true)
      }
    }
  })

  it.each(['oversized', 'duplicate', 'invalid'] as const)('blocks the whole selection for an %s user file before reading', async (reason) => {
    const file = directoryFile(reason === 'invalid' ? '../outside.json' : 'rejected.json', '{}')
    if (reason === 'oversized') {
      Object.defineProperty(file, 'size', { value: MAX_IMPORT_FILE_BYTES + 1 })
    }
    const read = vi.fn(async () => new TextEncoder().encode('{}').buffer)
    Object.defineProperty(file, 'arrayBuffer', { value: read })
    const client = fakeApi()
    render(<ProfileImport client={client} />)
    await act(async () => fireEvent.change(screen.getByLabelText('Choose configuration directory'), {
      target: { files: [directoryFile(reason === 'duplicate' ? 'rejected.json' : 'settings.json', '{}'), file] },
    }))
    await screen.findByRole('alert')
    expect(read).not.toHaveBeenCalled()
    const submit = screen.queryByRole('button', { name: 'Import configuration' })
    if (submit) expect(submit).toHaveProperty('disabled', true)
    expect(client.configImport).not.toHaveBeenCalled()
    expect(screen.queryByText(/Imported \d+ files/)).toBeNull()
  })

  it('rejects canonical aliases across prospective batches before reading or uploading', async () => {
    const read = vi.fn(async () => new ArrayBuffer(0))
    const files = [
      directoryFile('settings.json', ''),
      ...dependencyFiles(IMPORT_BATCH_FILES - 1),
      directoryFile('.claude/settings.json', ''),
    ]
    for (const file of [files[0], files[files.length - 1]]) {
      Object.defineProperty(file, 'arrayBuffer', { value: read })
    }
    const client = fakeApi()
    render(<ProfileImport client={client} />)
    await act(async () => fireEvent.change(screen.getByLabelText('Choose configuration directory'), {
      target: { files },
    }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/duplicate destination settings\.json/)
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    expect(read).not.toHaveBeenCalled()
    expect(client.configImport).not.toHaveBeenCalled()
  })

  it('rejects a file naming a custom destination root before any upload', async () => {
    const root = policyRoot({ path: '~/custom/agents/.claude' })
    const file = directoryFile('custom/agents/.claude', '')
    const read = vi.fn(async () => new ArrayBuffer(0))
    Object.defineProperty(file, 'arrayBuffer', { value: read })
    const client = fakeApi({
      configRoots: vi.fn(async () => ({ roots: [root] })),
    })
    render(<ProfileImport client={client} />)
    await act(async () => fireEvent.change(screen.getByLabelText('Choose configuration directory'), {
      target: { files: [directoryFile('settings.json', '{}'), file] },
    }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/destination root, not a file/)
    expect(screen.queryByRole('button', { name: 'Import configuration' })).toBeNull()
    expect(read).not.toHaveBeenCalled()
    expect(client.configImport).not.toHaveBeenCalled()
  })

  it('excludes credential and runtime files while preserving configuration', async () => {
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
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledWith({
      harness: 'claude',
      files: [{ path: 'settings.json', content_base64: 'e30=', mode: 0o644 }],
    }))
  })

  it('stops on a later read error while preserving confirmed files and bytes', async () => {
    const client = fakeApi({
      configImport: vi.fn(async ({ harness, files }) => ({
        harness, files: files.length, bytes: 2, excluded: [],
      })),
    })
    const bad = bufferedFile('unreadable.json', new ArrayBuffer(1))
    Object.defineProperty(bad, 'arrayBuffer', {
      value: vi.fn(async () => { throw new Error('The file is no longer available') }),
    })
    const last = bufferedFile('not-read.json', new ArrayBuffer(1))
    const readLast = vi.fn(async () => new ArrayBuffer(1))
    Object.defineProperty(last, 'arrayBuffer', { value: readLast })
    render(<ProfileImport client={client} />)
    await choose([
      directoryFile('settings.json', '{}'),
      ...dependencyFiles(IMPORT_BATCH_FILES - 1),
      bad,
      last,
    ])
    await confirmImport()
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('The file is no longer available')
    expect(alert.textContent).toContain(`${IMPORT_BATCH_FILES} files (2 B)`)
    expect(alert.textContent).not.toMatch(/unknown/i)
    expect(screen.getByText('settings.json')).toBeDefined()
    expect(screen.queryByText('unreadable.json')).toBeNull()
    expect(readLast).not.toHaveBeenCalled()
    expect(client.configImport).toHaveBeenCalledOnce()
  })

  it('recovers canonical committed paths for a custom root without stripping double prefixes twice', async () => {
    const prefix = 'custom/agents/.claude'
    const dependencies = dependencyFiles(IMPORT_BATCH_FILES - 3)
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [policyRoot({ path: `~/${prefix}` })],
      })),
      configImport: vi.fn().mockResolvedValueOnce({
        harness: 'claude', files: IMPORT_BATCH_FILES - 1, bytes: 4,
        excluded: [{ path: 'private.txt', reason: 'secret', detail: 'secret detected' }],
      }),
    })
    const bad = bufferedFile('unreadable.json', new ArrayBuffer(1))
    Object.defineProperty(bad, 'arrayBuffer', {
      value: async () => { throw new Error('The file is no longer available') },
    })
    render(<ProfileImport client={client} />)
    await choose([
      directoryFile(`${prefix}/settings.json`, '{}'),
      directoryFile(`${prefix}/${prefix}/settings.json`, '{}'),
      directoryFile(`${prefix}/private.txt`, 'secret'),
      ...dependencies,
      bad,
    ])
    await confirmImport()
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('The file is no longer available')
    expect(client.configImport).toHaveBeenCalledOnce()
    const sent = vi.mocked(client.configImport).mock.calls[0][0].files
    expect(sent.slice(0, 3).map(({ path }) => path)).toEqual([
      `${prefix}/settings.json`,
      `${prefix}/${prefix}/settings.json`,
      `${prefix}/private.txt`,
    ])
    const heading = screen.getByText(`Imported paths: ${IMPORT_BATCH_FILES - 1}`)
    const paths = Array.from(heading.parentElement!.querySelectorAll('li'), (item) => item.textContent)
    expect(paths).toEqual([
      'settings.json',
      `${prefix}/settings.json`,
      ...dependencies.map((file) => file.webkitRelativePath.slice('.claude/'.length)),
    ])
    expect(screen.getByText('private.txt')).toBeDefined()
    expect(screen.getByText(/secret detected/)).toBeDefined()
    expect(screen.queryByText(`${prefix}/private.txt`)).toBeNull()
  })

  it('stops before a third batch on a server partial failure and lists only exact committed paths', async () => {
    const dependencies = dependencyFiles(IMPORT_BATCH_FILES * 2)
    const excludedPath = 'agent/npm/node_modules/package/0.js'
    const committedPath = `agent/npm/node_modules/package/${IMPORT_BATCH_FILES}.js`
    const uncommittedPath = `agent/npm/node_modules/package/${IMPORT_BATCH_FILES + 1}.js`
    const client = fakeApi({
      configImport: vi.fn()
        .mockResolvedValueOnce({
          harness: 'claude', files: IMPORT_BATCH_FILES - 1, bytes: 0,
          excluded: [{ path: excludedPath, reason: 'secret', detail: 'secret detected' }],
        })
        .mockResolvedValueOnce({
          harness: 'claude', files: 1, bytes: 0, excluded: [],
          imported_paths: [committedPath], error: 'permission denied',
        }),
    })
    render(<ProfileImport client={client} />)
    await choose([...dependencies, directoryFile('settings.json', '{}')])
    await confirmImport()
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('permission denied')
    expect(alert.textContent).toContain(`${IMPORT_BATCH_FILES} files`)
    expect(client.configImport).toHaveBeenCalledTimes(2)
    const heading = screen.getByText(`Imported paths: ${IMPORT_BATCH_FILES}`)
    const paths = Array.from(heading.parentElement!.querySelectorAll('li'), (item) => item.textContent)
    expect(paths).toEqual([
      ...dependencies.slice(1, IMPORT_BATCH_FILES).map((file) => file.webkitRelativePath.slice('.claude/'.length)),
      committedPath,
    ])
    expect(paths).not.toContain(excludedPath)
    expect(paths).not.toContain(uncommittedPath)
    expect(screen.getByText(/secret detected/)).toBeDefined()
    expect(screen.queryByText('settings.json')).toBeNull()
  })

  it('distinguishes previously confirmed commits from an unknown batch after a lost response', async () => {
    const dependencies = dependencyFiles(IMPORT_BATCH_FILES * 2)
    const client = fakeApi({
      configImport: vi.fn()
        .mockResolvedValueOnce({ harness: 'claude', files: IMPORT_BATCH_FILES, bytes: 0, excluded: [] })
        .mockRejectedValueOnce(new Error('connection reset by peer')),
    })
    render(<ProfileImport client={client} />)
    await choose([...dependencies, directoryFile('settings.json', '{}')])
    await confirmImport()
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('connection reset by peer')
    expect(alert.textContent).toMatch(/unknown/i)
    expect(alert.textContent).toContain(`${IMPORT_BATCH_FILES} files`)
    expect(client.configImport).toHaveBeenCalledTimes(2)
    const heading = screen.getByText(`Imported paths: ${IMPORT_BATCH_FILES}`)
    expect(Array.from(heading.parentElement!.querySelectorAll('li'), (item) => item.textContent)).toEqual(
      dependencies.slice(0, IMPORT_BATCH_FILES).map((file) => file.webkitRelativePath.slice('.claude/'.length)),
    )
    const unknown = screen.getByText(`Paths with unknown outcome: ${IMPORT_BATCH_FILES}`)
    expect(Array.from(unknown.parentElement!.querySelectorAll('li'), (item) => item.textContent)).toEqual(
      dependencies.slice(IMPORT_BATCH_FILES).map((file) => file.webkitRelativePath.slice('.claude/'.length)),
    )
    expect(screen.queryByText('settings.json')).toBeNull()
  })
})

describe('directory import bounds', () => {
  it('ignores policy-excluded aliases before detecting destination collisions', async () => {
    const prepared = await prepareDirectoryImport(
      [
        directoryFile('auth.json', ''),
        directoryFile('.claude/auth.json', ''),
        directoryFile('debug/session.log', ''),
        directoryFile('.claude/debug/session.log', ''),
        directoryFile('settings.json', '{}'),
      ],
      policyRoot({ runtime_ignores: ['debug/'] }),
    )
    expect(prepared?.files.map(({ path }) => path)).toEqual(['settings.json'])
    expect(prepared?.excluded.map(({ path, reason }) => ({ path, reason }))).toEqual([
      { path: 'auth.json', reason: 'credential' },
      { path: '.claude/auth.json', reason: 'credential' },
      { path: 'debug/session.log', reason: 'runtime' },
      { path: '.claude/debug/session.log', reason: 'runtime' },
    ])
  })

  it('filters credential and destination-specific runtime metadata without reading any bytes', async () => {
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
    const prepared = await prepareDirectoryImport(
      [
        unreadable('.credentials.json', 'secret'),
        unreadable('agent/agent.db', 'secret'),
        unreadable('agent/agent.db-wal', 'secret'),
        unreadable('agent/agent.db-shm', 'secret'),
        ...runtime.map((path) => unreadable(path, 'runtime')),
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

  it('preserves binary and empty files at the upload boundary without reading excluded files', async () => {
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [policyRoot({ harness: 'omp', path: '~/.omp', runtime_ignores: ['agent/cache/'] })],
      })),
    })
    const excludedRead = vi.fn(async () => { throw new Error('excluded files must not be read') })
    const excluded = ['auth.json', 'agent/agent.db', 'agent/cache/state.json'].map((path) => {
      const file = directoryFile(path, 'private', '.omp')
      Object.defineProperty(file, 'arrayBuffer', { value: excludedRead })
      return file
    })
    render(<ProfileImport client={client} />)
    await choose([
      ...excluded,
      directoryFile('agent/extensions/fixture.bin', new Uint8Array([0, 255, 128, 1]), '.omp'),
      directoryFile('agent/skills/empty.md', '', '.omp'),
      directoryFile('agent/hooks/start.sh', '#!/bin/sh\n', '.omp'),
    ])
    await confirmImport()
    await screen.findByText(/Imported 3 files/)
    expect(excludedRead).not.toHaveBeenCalled()
    expect(client.configImport).toHaveBeenCalledWith({
      harness: 'omp',
      files: [
        { path: 'agent/extensions/fixture.bin', content_base64: 'AP+AAQ==', mode: 0o644 },
        { path: 'agent/skills/empty.md', content_base64: '', mode: 0o644 },
        { path: 'agent/hooks/start.sh', content_base64: 'IyEvYmluL3NoCg==', mode: 0o644 },
      ],
    })
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
