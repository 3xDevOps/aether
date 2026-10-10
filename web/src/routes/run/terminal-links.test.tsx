import { act, render, waitFor } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
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
  terminal_id: 'shell-1', incarnation: 'process-1', name: 'Shell',
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

function Shell() {
  const shells = useRunShells('run_1')
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
