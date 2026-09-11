import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import type { Api } from '@/lib/api'
import type { ConfigImportResult, GatewayCapabilities } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { AgentsStep } from '@/routes/onboarding/agents-step'
import { FirstRunStep } from '@/routes/onboarding/steps'
import { useStore } from '@/store'
import { capability } from '@/store/hooks'
import {
  alice,
  fakeApi,
  serverInfo,
  workspace,
} from '@/test/fixtures'
import {
  MAX_IMPORT_FILE_BYTES,
  ProfileImport,
  prepareDirectoryImport,
} from './profile-import'

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

async function choose(files: File[]) {
  const input = screen.getByLabelText('Choose configuration directory')
  fireEvent.change(input, { target: { files } })
  await waitFor(() => expect(screen.getByText('Preview')).toBeDefined())
}

async function confirmImport() {
  const button = screen.getByRole('button', { name: 'Import configuration' })
  await waitFor(() => expect(button).toHaveProperty('disabled', false))
  fireEvent.click(button)
}

beforeEach(() => {
  vi.restoreAllMocks()
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
    const oldFile = directoryFile('old.md', 'old')
    Object.defineProperty(oldFile, 'arrayBuffer', {
      configurable: true,
      value: () => pending.promise,
    })
    const input = screen.getByLabelText('Choose configuration directory')
    fireEvent.change(input, { target: { files: [oldFile] } })
    await screen.findByRole('status')

    fireEvent.change(input, {
      target: { files: [directoryFile('new.md', 'new')] },
    })
    await waitFor(() => expect(screen.getByText('Preview')).toBeDefined())
    expect(screen.queryByText(/old.md/)).toBeNull()
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledTimes(1))
    expect(client.configImport).toHaveBeenCalledWith({
      harness: 'claude',
      files: [{ path: 'new.md', content_base64: 'bmV3', mode: 0o644 }],
    })
    pending.resolve(new TextEncoder().encode('old').buffer)
  })

  it('requires a destination when the selected basename is ambiguous', async () => {
    const client = fakeApi({
      configRoots: vi.fn(async () => ({
        roots: [
          { harness: 'claude', path: '~/.shared' },
          { harness: 'codex', path: '~/.shared' },
        ],
      })),
    })
    renderStep(client)
    const file = directoryFile('settings.json', '{}')
    Object.defineProperty(file, 'webkitRelativePath', {
      configurable: true,
      value: 'shared/settings.json',
    })
    await choose([file])

    const destination = screen.getByLabelText('Configuration destination')
    expect((screen.getByRole('button', { name: 'Import configuration' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(destination, { target: { value: 'codex' } })
    expect((screen.getByRole('button', { name: 'Import configuration' }) as HTMLButtonElement).disabled).toBe(false)
    await confirmImport()
    await waitFor(() => expect(client.configImport).toHaveBeenCalledTimes(1))
    expect(client.configImport).toHaveBeenCalledWith({
      harness: 'codex',
      files: [{ path: 'settings.json', content_base64: 'e30=', mode: 0o644 }],
    })
  })

  it('shows server exclusions with their actual reason after the one import', async () => {
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

  it('leaves the optional skip action available with an import result or error', async () => {
    const client = fakeApi()
    const { onNext } = renderStep(client)
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalledTimes(1)
  })
})

describe('directory import bounds', () => {
  it('filters credentials, runtime history and oversized files before reading', async () => {
    const root = 'renamed-agent-home'
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
      'history.jsonl',
      'agent/sessions/transcript.jsonl',
      'agent/tmp/download.tgz',
      'agent/terminal-sessions/terminal.json',
      'agent/cache/state.json',
      'agent/history.db',
      'agent/history.db-shm',
      'agent/history.db-wal',
      'agent/models.db',
      'natives/18.1.4/node',
      'cache/runtime.json',
      'logs/omp.log',
      'run/state.json',
      'collab/transcript.jsonl',
    ]
    const oversized = unreadable(
      'big.bin',
      new Uint8Array(MAX_IMPORT_FILE_BYTES + 1),
    )
    const prepared = await prepareDirectoryImport([
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
    ])

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
