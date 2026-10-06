import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import type { DevTerminal } from '@/lib/types'
import { RunDock } from '@/routes/terminal/run-dock'
import { useStore } from '@/store'
import { initialRunShellDock, unregisterShellSocket } from '@/store/terminal'
import { run } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const process: DevTerminal = {
  terminal_id: 'command-1', incarnation: 'process-1', name: 'Agent command',
  cols: 73, rows: 19, process: { state: 'running' },
}

beforeEach(() => {
  StubSocket.install()
  useStore.getState().upsertRun(run())
  useStore.setState({ pausedRuns: { run_1: false }, shellDocks: {
    run_1: { ...initialRunShellDock, collapsed: false },
  } })
  vi.spyOn(api, 'devTerminalList').mockResolvedValue({ terminals: [process] })
  vi.spyOn(api, 'devTerminalStart').mockResolvedValue({ terminal: process })
  vi.spyOn(api, 'devControlStatus').mockResolvedValue({
    surface: { kind: 'terminal', id: process.terminal_id, incarnation: process.incarnation },
    controller: null,
  })
  vi.spyOn(api, 'devTerminalResize').mockResolvedValue({ terminal: process, screen_revision: 1, geometry_revision: 1 })
  vi.spyOn(api, 'devTerminalInput').mockResolvedValue({ accepted: true })
  vi.spyOn(api, 'devTerminalStop').mockResolvedValue({ terminal: { ...process, process: { state: 'stopped' } }, stopped: true, timed_out: false })
})
afterEach(() => {
  unregisterShellSocket('run_1', process.terminal_id)
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

async function attach(write = false, replay = '') {
  await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
  const socket = StubSocket.last()
  act(() => {
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({
      ok: true, cols: 73, rows: 19, replay: new TextEncoder().encode(replay).length,
      terminal_id: process.terminal_id, incarnation: process.incarnation,
      server_owned_responder: true, has_control: write, control_generation: 7,
    }) })
    if (replay) socket.onmessage?.({ data: new TextEncoder().encode(replay).buffer })
  })
  return socket
}

async function terminalActions() {
  fireEvent.keyDown(screen.getByRole('button', { name: 'More terminal actions' }), { key: 'ArrowDown' })
  return within(await screen.findByRole('menu'))
}

it('keeps evidence reachable with an empty collapsed dock without creating a terminal', async () => {
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [] })
  vi.spyOn(api, 'runEvidenceList').mockResolvedValue({ packets: [] })
  vi.spyOn(api, 'devArtifactList').mockResolvedValue({ artifacts: [], truncated: false })
  useStore.getState().setDockCollapsed('run_1', true)
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  const evidence = screen.getByRole('button', { name: 'Evidence' })
  evidence.focus()
  fireEvent.click(evidence)
  const panel = await screen.findByRole('dialog', { name: 'Retained evidence' })
  expect(screen.queryByRole('tabpanel')).toBeNull()
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  expect(StubSocket.opened).toHaveLength(0)
  fireEvent.click(within(panel).getByRole('button', { name: 'Close evidence' }))
  await waitFor(() => expect(document.activeElement).toBe(evidence))
  fireEvent.click(screen.getByRole('button', { name: 'Expand terminal dock' }))
  expect(screen.getAllByRole('button', { name: 'Evidence' })).toHaveLength(1)
  fireEvent.click(evidence)
  await screen.findByRole('dialog', { name: 'Retained evidence' })
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  expect(StubSocket.opened).toHaveLength(0)
  view.unmount()
})

it('discovers an agent command without creating a process or resizing a watcher', async () => {
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  const socket = await attach(false, '\x1b[?1049h\x1b[H共有 λ界')
  expect(socket.frames()[0]).toMatchObject({ incarnation: 'process-1', follow: true })
  expect(socket.frames()[0]).not.toHaveProperty('write')
  await waitFor(() => expect(view.container.querySelector('.xterm-rows')?.textContent).toContain('共有 λ界'))
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  expect(api.devTerminalResize).not.toHaveBeenCalled()
  view.unmount()
})

it('hides and rejoins the same incarnation without stopping or restarting it', async () => {
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  const first = await attach()
  fireEvent.click((await terminalActions()).getByRole('menuitem', { name: 'Hide terminal' }))
  expect(first.closed).toBe(true)
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: /Show Agent command/ }))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  const second = await attach()
  expect(second.frames()[0]).toMatchObject({ incarnation: 'process-1' })
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  view.unmount()
})

it('returns focus to More when hiding a running shell selects an exited sibling', async () => {
  const ended: DevTerminal = {
    ...process, terminal_id: 'command-ended', incarnation: 'process-ended',
    name: 'Finished command', process: { state: 'exited', exit_code: 0 },
  }
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [process, ended] })
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  const socket = await attach()
  await screen.findByRole('tab', { name: /Finished command.*exited/ })
  fireEvent.click((await terminalActions()).getByRole('menuitem', { name: 'Hide terminal' }))
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'More terminal actions' })))
  expect(screen.getByRole('tab', { name: /Finished command.*exited/ }).getAttribute('aria-selected')).toBe('true')
  expect(socket.closed).toBe(true)
  expect(StubSocket.opened).toHaveLength(1)
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  view.unmount()
})

it('requires confirmed authority before resizing or stopping a process', async () => {
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  await attach()
  const stop = (await terminalActions()).getByRole('menuitem', { name: 'Stop terminal' })
  expect(stop.getAttribute('aria-disabled')).toBe('true')
  fireEvent.keyDown(stop, { key: 'Escape' })
  await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
  fireEvent.click(screen.getByRole('button', { name: 'Take shell control' }))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  expect(api.devTerminalResize).not.toHaveBeenCalled()
  await attach(true)
  fireEvent.click((await terminalActions()).getByRole('menuitem', { name: 'Stop terminal' }))
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'More terminal actions' })))
  expect(api.devTerminalStop).not.toHaveBeenCalled()
  fireEvent.click((await terminalActions()).getByRole('menuitem', { name: 'Stop terminal' }))
  fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm stop' }))
  await waitFor(() => expect(api.devTerminalStop).toHaveBeenCalledWith(expect.objectContaining({
    terminal_id: process.terminal_id, incarnation: process.incarnation, control_generation: 7,
  })))
  view.unmount()
})

it('keeps ended command terminals discoverable without attaching or rerunning them', async () => {
  vi.mocked(api.devTerminalList).mockResolvedValue({ terminals: [{ ...process, process: { state: 'exited', exit_code: 23 } }] })
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  await screen.findByRole('tab', { name: /Agent command.*exited/ })
  expect((await terminalActions()).getByRole('menuitem', { name: 'Stop terminal' }).getAttribute('aria-disabled')).toBe('true')
  expect(StubSocket.opened).toHaveLength(0)
  expect(api.devTerminalStart).not.toHaveBeenCalled()
  view.unmount()
})

it('refuses an ACK for a replacement incarnation rather than parsing its output', async () => {
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
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
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  await attach()
  fireEvent.click(screen.getByRole('button', { name: 'Take shell control' }))
  await waitFor(() => expect(StubSocket.opened).toHaveLength(2))
  await attach(true)
  await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull())
  const input = view.container.querySelector('.xterm-helper-textarea')!
  fireEvent.paste(input, { clipboardData: { getData: () => 'x'.repeat(5000) } })
  await waitFor(() => expect(api.devTerminalInput).toHaveBeenCalledTimes(1))
  if (boundary === 'detach') {
    fireEvent.click((await terminalActions()).getByRole('menuitem', { name: 'Hide terminal' }))
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
  const view = render(<RunDock runID="run_1" onEvidenceAnswer={vi.fn()} />)
  await waitFor(() => expect(api.devControlStatus).toHaveBeenCalledWith({
    run_id: 'run_1', surface: { kind: 'terminal', id: process.terminal_id, incarnation: process.incarnation },
  }))
  await act(async () => { await Promise.resolve() })
  expect(api.devTerminalList).toHaveBeenCalledTimes(1)
  expect(api.devControlStatus).toHaveBeenCalledTimes(1)
  view.unmount()
})
