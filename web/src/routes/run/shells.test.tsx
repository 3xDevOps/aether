import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import type { DevController, DevTerminal } from '@/lib/types'
import { ShellTerminal } from '@/routes/run/shell-terminal'
import { StopShellDialog, TerminalTabs, useRunShells } from '@/routes/run/shells'
import { useStore } from '@/store'
import { initialRunShellDock, unregisterShellSocket } from '@/store/terminal'
import { alice, run, serverInfo } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const process: DevTerminal = {
  terminal_id: 'command-1', incarnation: 'process-1', name: 'Agent command', started_by: 'run_agent',
  cols: 73, rows: 19, process: { state: 'running' },
}
const surface = { kind: 'terminal' as const, id: process.terminal_id, incarnation: process.incarnation }
const agentController: DevController = {
  kind: 'run_agent', run_id: 'run_1', control_session_id: 'agent-session', control_generation: 3,
  connected: true, acquired_at: '2026-08-14T10:03:00Z',
}
const ownShell = (n: number): DevTerminal => ({
  ...process, terminal_id: `tab-${n}`, incarnation: `shell-${n}`, name: `tab-${n}`, started_by: 'member',
})

beforeEach(() => {
  StubSocket.install()
  useStore.getState().upsertRun(run())
  useStore.setState({ info: serverInfo, pausedRuns: { run_1: false }, shellDocks: {
    run_1: { ...initialRunShellDock, shellShown: true },
  } })
  vi.spyOn(api, 'devTerminalList').mockResolvedValue({ terminals: [process] })
  vi.spyOn(api, 'devTerminalStart').mockResolvedValue({ terminal: process })
  vi.spyOn(api, 'devControlStatus').mockResolvedValue({ surface, controller: null })
  vi.spyOn(api, 'devControlAcquire').mockResolvedValue({
    surface, controller: { ...agentController, kind: 'member', control_session_id: 'stop-session', control_generation: 9 },
  })
  vi.spyOn(api, 'devControlRelease').mockResolvedValue({ released: true })
  vi.spyOn(api, 'devTerminalResize').mockResolvedValue({ terminal: process, screen_revision: 1, geometry_revision: 1 })
  vi.spyOn(api, 'devTerminalInput').mockResolvedValue({ accepted: true })
  vi.spyOn(api, 'devTerminalStop').mockResolvedValue({ terminal: { ...process, process: { state: 'stopped' } }, stopped: true, timed_out: false })
})
afterEach(() => {
  for (const tab of [process.terminal_id, 'tab-1']) unregisterShellSocket('run_1', tab)
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

async function attach(write = false, replay = '', terminal = process) {
  await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
  const socket = StubSocket.last()
  act(() => {
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({
      ok: true, cols: 73, rows: 19, replay: new TextEncoder().encode(replay).length,
      terminal_id: terminal.terminal_id, incarnation: terminal.incarnation,
      server_owned_responder: true, has_control: write, control_generation: 7,
    }) })
    if (replay) socket.onmessage?.({ data: new TextEncoder().encode(replay).buffer })
  })
  return socket
}

async function menu(name: string | RegExp) {
  fireEvent.keyDown(screen.getByRole('button', { name }), { key: 'ArrowDown' })
  return within(await screen.findByRole('menu'))
}

async function type(view: ReturnType<typeof render>, text: string) {
  await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull())
  fireEvent.paste(view.container.querySelector('.xterm-helper-textarea')!, { clipboardData: { getData: () => text } })
}

function Shells({ visible = true }: { visible?: boolean }) {
  const shells = useRunShells('run_1', visible)
  const tabs = <TerminalTabs shells={shells} agent />
  return (
    <>
      {shells.error && <p role="alert">{shells.error}</p>}
      {shells.dock.shellShown && shells.dock.activeTab ? <ShellTerminal shells={shells} tabs={tabs} onCaptures={vi.fn()} /> : tabs}
      <StopShellDialog shells={shells} />
    </>
  )
}

it('discovers an agent command without creating a process, taking control or resizing a watcher', async () => {
  const view = render(<Shells />)
  const socket = await attach(false, '\x1b[?1049h\x1b[H共有 λ界')
  expect(socket.frames()[0]).toMatchObject({ incarnation: 'process-1', follow: true })
  expect(socket.frames()[0]).not.toHaveProperty('write')
  await waitFor(() => expect(view.container.querySelector('.xterm-rows')?.textContent).toContain('共有 λ界'))
  expect(screen.getByRole('tab', { name: 'Agent command, started by the agent' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Take control' })).toBeNull()
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  expect(api.devTerminalResize).not.toHaveBeenCalled()
  view.unmount()
})

it('opens a new shell already asking for control', async () => {
  const shell = ownShell(1)
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [] })
  vi.mocked(api.devTerminalStart).mockResolvedValue({ terminal: shell })
  const view = render(<Shells />)
  fireEvent.click(await screen.findByRole('button', { name: 'New shell' }))
  const socket = await attach(true, '', shell)
  expect(socket.frames()[0]).toMatchObject({ incarnation: 'shell-1', write: true })
  expect(await screen.findByRole('tab', { name: 'Shell 1' })).toBeTruthy()
  expect(await screen.findByText('You control')).toBeTruthy()
  view.unmount()
})

it('takes control on the first key in a shell nobody drives, then sends what was typed', async () => {
  const view = render(<Shells />)
  await attach()
  await type(view, 'ls')
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  expect(api.devTerminalInput).not.toHaveBeenCalled()
  await type(view, ' -la')
  const socket = await attach(true)
  expect(socket.frames()[0]).toMatchObject({ incarnation: 'process-1', write: true })
  expect(socket.frames()[0]).not.toHaveProperty('takeover')
  await waitFor(() => expect(api.devTerminalInput).toHaveBeenCalledWith(expect.objectContaining({
    terminal_id: process.terminal_id, incarnation: process.incarnation, control_generation: 7, kind: 'text', text: 'ls -la',
  })))
  expect(StubSocket.opened).toHaveLength(2)
  fireEvent.click(await screen.findByRole('button', { name: 'Release' }))
  await waitFor(() => expect(api.devControlRelease).toHaveBeenCalledWith(expect.objectContaining({ surface, control_generation: 7 })))
  view.unmount()
})

it('does not take control for a focus or mouse report, which xterm sends without a key press', async () => {
  const view = render(<Shells />)
  await attach()
  await type(view, '\x1b[I')
  await type(view, '\x1b[<35;12;4M')
  await act(async () => { await Promise.resolve() })
  expect(StubSocket.opened).toHaveLength(1)
  await type(view, '\x1b[A')
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  view.unmount()
})

it('drops what was typed when the server gives the shell to someone else first', async () => {
  const view = render(<Shells />)
  await attach()
  await type(view, 'rm -rf build\r')
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  vi.mocked(api.devControlStatus).mockResolvedValue({ surface, controller: agentController })
  const refused = StubSocket.last()
  act(() => {
    refused.onopen?.()
    refused.onmessage?.({ data: JSON.stringify({ ok: false, code: -32003, error: 'run control is held by another session' }) })
    refused.onclose?.({ code: 1008 })
  })
  expect(await screen.findByText('The agent controls')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Take control' })).toBeTruthy()
  await waitFor(() => expect(StubSocket.opened).toHaveLength(3))
  const mirror = await attach()
  expect(mirror.frames()[0]).not.toHaveProperty('write')
  expect(api.devTerminalInput).not.toHaveBeenCalled()
  view.unmount()
})

it('names who controls a shell and asks before taking it from them', async () => {
  vi.mocked(api.devControlStatus).mockResolvedValue({ surface, controller: agentController })
  const view = render(<Shells />)
  await attach()
  expect(await screen.findByText('The agent controls')).toBeTruthy()
  await type(view, 'ls')
  expect(StubSocket.opened).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: 'Take control' }))
  const dialog = within(await screen.findByRole('dialog', { name: 'Take control of this shell?' }))
  expect(dialog.getByText(/The agent controls this shell/)).toBeTruthy()
  fireEvent.click(dialog.getByRole('button', { name: 'Take control' }))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  const socket = await attach(true)
  expect(socket.frames()[0]).toMatchObject({ write: true, takeover: true, control_generation: 3 })
  view.unmount()
})

it('takes back a lease that a closed page of the same member left behind', async () => {
  vi.mocked(api.devControlStatus).mockResolvedValue({ surface, controller: {
    kind: 'member', member_id: alice.id, control_session_id: 'before-reload', control_generation: 5,
    connected: false, acquired_at: '2026-08-14T10:03:00Z', expires_at: '2026-08-14T10:03:15Z',
  } })
  const view = render(<Shells />)
  await attach()
  await waitFor(() => expect(api.devControlStatus).toHaveBeenCalled())
  expect(screen.queryByRole('button', { name: 'Take control' })).toBeNull()
  await type(view, 'ls')
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  expect((await attach(true)).frames()[0]).toMatchObject({ write: true, takeover: true, control_generation: 5 })
  view.unmount()
})

it('never takes control by typing in a swarm worker, where control is also a durable hold', async () => {
  useStore.getState().upsertRun(run({ mission_id: 'mission_1', mission_role: 'worker' }))
  const view = render(<Shells />)
  await attach()
  expect(await screen.findByText(/This run is a swarm worker/)).toBeTruthy()
  await type(view, 'ls')
  expect(StubSocket.opened).toHaveLength(1)
  const stop = (await menu('Shell actions')).getByRole('menuitem', { name: /Stop shell/ })
  expect(stop.getAttribute('aria-disabled')).toBe('true')
  expect(stop.textContent).toContain('Take control first')
  fireEvent.keyDown(stop, { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'Take control' }))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  expect((await attach(true)).frames()[0]).toMatchObject({ write: true })
  view.unmount()
})

it('releases control when the Terminal view is left', async () => {
  const view = render(<Shells />)
  await attach()
  await type(view, 'ls')
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  await attach(true)
  await screen.findByText('You control')
  view.rerender(<Shells visible={false} />)
  await waitFor(() => expect(api.devControlRelease).toHaveBeenCalledWith(expect.objectContaining({ surface, control_generation: 7 })))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(3))
  expect((await attach()).frames()[0]).not.toHaveProperty('write')
  view.unmount()
})

it('hides a running shell from its tab and brings it back from the hidden count', async () => {
  const view = render(<Shells />)
  const first = await attach()
  fireEvent.click((await menu('Close Agent command')).getByRole('menuitem', { name: 'Hide, keep running' }))
  expect(first.closed).toBe(true)
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  expect(screen.queryByRole('tab', { name: /Agent command/ })).toBeNull()
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'New shell' })))
  fireEvent.click((await menu('1 hidden shell')).getByRole('menuitem', { name: 'Show Agent command' }))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  const second = await attach()
  expect(second.frames()[0]).toMatchObject({ incarnation: 'process-1' })
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  view.unmount()
})

it('returns focus to Shell actions when hiding the shown shell leaves an ended one', async () => {
  const sibling: DevTerminal = { ...process, terminal_id: 'command-2', incarnation: 'process-2', name: 'Sibling' }
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [process, sibling] })
  const view = render(<Shells />)
  const socket = await attach()
  await screen.findByRole('tab', { name: /Sibling/ })
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [process, { ...sibling, process: { state: 'exited', exit_code: 0 } }] })
  await act(async () => { await useStore.getState().syncShellTerminals('run_1', (await api.devTerminalList({ run_id: 'run_1' })).terminals) })
  fireEvent.click((await menu('Shell actions')).getByRole('menuitem', { name: 'Hide, keep running' }))
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Shell actions' })))
  expect(screen.getByRole('tab', { name: /Sibling · exited/ }).getAttribute('aria-selected')).toBe('true')
  expect(screen.getByText(/This shell exited with code 0\./)).toBeTruthy()
  expect(socket.closed).toBe(true)
  expect(StubSocket.opened).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: 'Close Sibling' }))
  expect(screen.queryByRole('tab', { name: /Sibling/ })).toBeNull()
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  view.unmount()
})

it('stops a shell nobody controls from its tab, taking and returning control around the stop', async () => {
  const view = render(<Shells />)
  await attach()
  fireEvent.click((await menu('Close Agent command')).getByRole('menuitem', { name: 'Stop shell' }))
  await waitFor(() => expect(api.devTerminalStop).toHaveBeenCalledWith({
    run_id: 'run_1', terminal_id: process.terminal_id, incarnation: process.incarnation,
    control_session_id: 'stop-session', control_generation: 9, timeout_ms: 3000,
  }))
  expect(vi.mocked(api.devControlAcquire).mock.calls[0][0]).not.toHaveProperty('takeover')
  await waitFor(() => expect(api.devControlRelease).toHaveBeenCalledWith({
    run_id: 'run_1', surface, control_session_id: 'stop-session', control_generation: 9,
  }))
  await waitFor(() => expect(screen.queryByRole('tab', { name: /Agent command/ })).toBeNull())
  expect(screen.queryByRole('dialog')).toBeNull()
  view.unmount()
})

it('stops under the lease this page already holds', async () => {
  const view = render(<Shells />)
  await attach()
  await type(view, 'q')
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  await attach(true)
  await screen.findByText('You control')
  fireEvent.click((await menu('Shell actions')).getByRole('menuitem', { name: 'Stop shell' }))
  await waitFor(() => expect(api.devTerminalStop).toHaveBeenCalledWith(expect.objectContaining({ control_generation: 7 })))
  expect(api.devControlAcquire).not.toHaveBeenCalled()
  view.unmount()
})

it('asks before stopping a shell someone else controls, and names them', async () => {
  vi.mocked(api.devControlStatus).mockResolvedValue({ surface, controller: agentController })
  const view = render(<Shells />)
  await attach()
  fireEvent.click((await menu('Close Agent command')).getByRole('menuitem', { name: 'Stop shell' }))
  const dialog = within(await screen.findByRole('dialog', { name: 'Stop Agent command?' }))
  expect(dialog.getByText(/The agent controls this shell/)).toBeTruthy()
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  fireEvent.click(dialog.getByRole('button', { name: 'Stop shell' }))
  await waitFor(() => expect(api.devControlAcquire).toHaveBeenCalledWith(expect.objectContaining({ takeover: true, expected_generation: 3 })))
  await waitFor(() => expect(api.devTerminalStop).toHaveBeenCalledWith(expect.objectContaining({ control_generation: 9 })))
  view.unmount()
})

it('counts only shells people started against the limit, and says so in place', async () => {
  const agents = [1, 2, 3, 4].map((n): DevTerminal => ({ ...process, terminal_id: `agent-${n}`, incarnation: `agent-${n}`, name: `agent-${n}` }))
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [...agents, ownShell(1), ownShell(2), { ...ownShell(3), process: { state: 'exited', exit_code: 0 } }] })
  useStore.setState({ shellDocks: { run_1: initialRunShellDock } })
  const view = render(<Shells />)
  await screen.findByRole('tab', { name: 'Shell 2' })
  expect(screen.getByRole('button', { name: 'New shell' }).getAttribute('aria-disabled')).toBeNull()
  expect(screen.queryByRole('tab', { name: /Shell 3/ })).toBeNull()
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [...agents, ownShell(1), ownShell(2), ownShell(4), ownShell(5)] })
  fireEvent.click(screen.getByRole('button', { name: 'New shell' }))
  await waitFor(() => expect(api.devTerminalStart).toHaveBeenCalledTimes(1))
  await act(async () => { await useStore.getState().syncShellTerminals('run_1', (await api.devTerminalList({ run_id: 'run_1' })).terminals) })
  expect(screen.getByRole('button', { name: 'New shell' }).getAttribute('aria-disabled')).toBe('true')
  expect(screen.getByText('At most 4 shells besides the agent’s')).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'New shell' }))
  expect(api.devTerminalStart).toHaveBeenCalledTimes(1)
  view.unmount()
})

it('refuses an ACK for a replacement incarnation rather than parsing its output', async () => {
  const view = render(<Shells />)
  await waitFor(() => expect(StubSocket.opened).toHaveLength(1))
  const socket = StubSocket.last()
  act(() => {
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({
      ok: true, terminal_id: process.terminal_id, incarnation: 'replacement', server_owned_responder: true,
    }) })
  })
  expect(socket.closed).toBe(true)
  expect(api.devTerminalResize).not.toHaveBeenCalled()
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  view.unmount()
})

it.each(['detach', 'rejection'])('drops the unsent remainder of a paste after %s', async (boundary) => {
  let finish!: (value: { accepted: boolean }) => void
  let reject!: (reason: Error) => void
  const pending = new Promise<{ accepted: boolean }>((resolve, fail) => { finish = resolve; reject = fail })
  vi.mocked(api.devTerminalInput).mockReturnValueOnce(pending)
  const view = render(<Shells />)
  await attach()
  await type(view, 'x'.repeat(5000))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  await attach(true)
  await waitFor(() => expect(api.devTerminalInput).toHaveBeenCalledTimes(1))
  if (boundary === 'detach') {
    fireEvent.click((await menu('Shell actions')).getByRole('menuitem', { name: 'Hide, keep running' }))
    await act(async () => { finish({ accepted: true }); await pending })
  } else {
    await act(async () => { reject(new Error('Control was fenced')); await pending.catch(() => {}) })
    expect((await screen.findByRole('alert')).textContent).toContain('Control was fenced')
  }
  expect(api.devTerminalInput).toHaveBeenCalledTimes(1)
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  view.unmount()
})

it('lists shells once on open and reads control for the tab that list selects', async () => {
  const view = render(<Shells />)
  await waitFor(() => expect(api.devControlStatus).toHaveBeenCalledWith({ run_id: 'run_1', surface }))
  await act(async () => { await Promise.resolve() })
  expect(api.devTerminalList).toHaveBeenCalledTimes(1)
  expect(api.devControlStatus).toHaveBeenCalledTimes(1)
  view.unmount()
})

it('polls every two seconds while the Terminal view shows and every ten behind another view', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  try {
    const view = render(<Shells />)
    await waitFor(() => expect(api.devControlStatus).toHaveBeenCalledTimes(1))
    vi.mocked(api.devTerminalList).mockRejectedValue(new Error('dev.terminal.list: run is not running'))
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000) })
    await waitFor(() => expect(api.devControlStatus).toHaveBeenCalledTimes(2))
    expect(api.devTerminalList).toHaveBeenCalledTimes(2)
    expect((await screen.findByRole('alert')).textContent).toContain('run is not running')
    view.rerender(<Shells visible={false} />)
    await waitFor(() => expect(api.devTerminalList).toHaveBeenCalledTimes(3))
    await act(async () => { await vi.advanceTimersByTimeAsync(8_000) })
    expect(api.devTerminalList).toHaveBeenCalledTimes(3)
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000) })
    await waitFor(() => expect(api.devTerminalList).toHaveBeenCalledTimes(4))
    view.unmount()
  } finally {
    vi.useRealTimers()
  }
})
