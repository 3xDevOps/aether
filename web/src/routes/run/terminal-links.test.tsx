import { act, render, waitFor } from '@testing-library/react'
import { type ILink, Terminal } from '@xterm/xterm'
import { api } from '@/lib/api'
import type { DevTerminal } from '@/lib/types'
import { lookupRoute } from '@/routes/registry'
import '@/routes/run'
import { ShellTerminal } from '@/routes/run/shell-terminal'
import { TerminalTabs, useRunShells } from '@/routes/run/shells'
import { useStore } from '@/store'
import { initialRunShellDock, unregisterShellSocket } from '@/store/terminal'
import { alice, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const shell: DevTerminal = {
  terminal_id: 'shell-1', incarnation: 'process-1', name: 'Shell', started_by: 'member',
  cols: 80, rows: 24, process: { state: 'running' },
}
const range = { start: { x: 0, y: 0 }, end: { x: 8, y: 0 } }

function activate(terminal: Terminal, uri: string) {
  act(() => terminal.options.linkHandler?.activate(new MouseEvent('click'), uri, range))
}

beforeEach(() => {
  StubSocket.install()
  useStore.getState().upsertRun(run())
  useStore.setState({
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    browserRequests: {},
    runViewMemory: {},
    pausedRuns: { run_1: false },
    shellDocks: { run_1: { ...initialRunShellDock, shellShown: true } },
  })
  vi.spyOn(api, 'runRoomList').mockResolvedValue({ messages: [] })
  vi.spyOn(api, 'runRoomStatus').mockRejectedValue(new Error('not asked here'))
  vi.spyOn(api, 'agentList').mockResolvedValue([])
  vi.spyOn(api, 'devBrowserStatus').mockResolvedValue({ available: false, running: false, state: 'unavailable', reason: 'no browser in this test' })
  vi.spyOn(api, 'devTerminalList').mockResolvedValue({ terminals: [shell] })
  vi.spyOn(api, 'devControlStatus').mockResolvedValue({
    surface: { kind: 'terminal', id: shell.terminal_id, incarnation: shell.incarnation },
    controller: null,
  })
})
afterEach(() => {
  unregisterShellSocket('run_1', shell.terminal_id)
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('opens a run-local link from the agent terminal in the run’s Browser and no other link', async () => {
  const opened = vi.spyOn(window, 'open').mockImplementation(() => null)
  const links = vi.spyOn(Terminal.prototype, 'registerLinkProvider')
  const View = lookupRoute('run')!
  useStore.setState({ route: { name: 'run', params: { runId: 'run_1', view: 'terminal' } } })
  const view = render(<View params={{ runId: 'run_1', view: 'terminal' }} />)
  await waitFor(() => expect(links).toHaveBeenCalled())
  const terminal = links.mock.contexts[0] as Terminal

  activate(terminal, 'https://example.com/docs')
  expect(opened).toHaveBeenCalledWith('https://example.com/docs', '_blank', 'noopener,noreferrer')
  // An OAuth callback is still taken by the forward instructions, not the Browser.
  activate(terminal, 'http://localhost:1455/auth/callback?code=1')
  expect(useStore.getState().browserRequests).toEqual({})
  expect(useStore.getState().route.params.view).toBe('terminal')

  activate(terminal, 'http://localhost:3000/app')
  expect(opened).toHaveBeenCalledTimes(1)
  expect(useStore.getState().route.params.view).toBe('browser')
  view.unmount()
})

it('links a path a run terminal printed once it is a file in the checkout, and opens it in Files at its line', async () => {
  const listings: Record<string, { name: string; kind: 'file' | 'dir'; size: number }[]> = {
    '': [{ name: 'src', kind: 'dir', size: 0 }],
    src: [{ name: 'app.ts', kind: 'file', size: 1 }],
  }
  vi.spyOn(api, 'filesTree').mockImplementation(async ({ path }) => ({ entries: listings[path] ?? [] }))
  vi.spyOn(api, 'filesRead').mockResolvedValue({ content: 'one\n', truncated: false, binary: false, size: 4, revision: 'r1', writable: true })
  const links = vi.spyOn(Terminal.prototype, 'registerLinkProvider')
  const View = lookupRoute('run')!
  useStore.setState({ route: { name: 'run', params: { runId: 'run_1', view: 'terminal' } } })
  const view = render(<View params={{ runId: 'run_1', view: 'terminal' }} />)
  await waitFor(() => expect(links).toHaveBeenCalled())
  const terminal = links.mock.contexts[0] as Terminal
  // The second row is the wrapped end of an 80-column line.
  await new Promise<void>((done) => terminal.write(`${'x'.repeat(70)} src/app.ts:12:3: error and src/gone.ts:1\r\n`, done))

  const found = await Promise.all(links.mock.calls.map(([provider]) =>
    new Promise<ILink[] | undefined>((reply) => provider.provideLinks(2, reply))))
  const [link, ...others] = found.flatMap((reply) => reply ?? [])
  expect(others).toEqual([])
  expect(link).toMatchObject({ text: 'src/app.ts:12:3', range: { start: { x: 72, y: 1 }, end: { x: 6, y: 2 } } })

  act(() => link!.activate(new MouseEvent('click'), link!.text))
  expect(useStore.getState().route.params).toEqual({ runId: 'run_1', view: 'files', file: 'src/app.ts', line: '12', column: '3' })
  view.unmount()
})

function Shell() {
  const shells = useRunShells('run_1', true)
  return <ShellTerminal shells={shells} tabs={<TerminalTabs shells={shells} agent />} onCaptures={vi.fn()} />
}

it('hands a run-local link from a run shell to the run’s Browser', async () => {
  const opened = vi.spyOn(window, 'open').mockImplementation(() => null)
  const links = vi.spyOn(Terminal.prototype, 'registerLinkProvider')
  const view = render(<Shell />)
  await waitFor(() => expect(links).toHaveBeenCalled())
  const terminal = links.mock.contexts[0] as Terminal

  activate(terminal, 'http://127.0.0.1:5173/')
  expect(useStore.getState().browserRequests).toEqual({ run_1: 'http://127.0.0.1:5173/' })
  expect(opened).not.toHaveBeenCalled()
  activate(terminal, 'https://example.com/')
  expect(opened).toHaveBeenCalledWith('https://example.com/', '_blank', 'noopener,noreferrer')
  view.unmount()
})
