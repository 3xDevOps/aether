import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { Terminal } from '@xterm/xterm'
import type { XtermController } from '@/components/xterm-host'
import type { TerminalReadSurface } from '@/components/terminal-pane'
import { captureTerminalPresentation, type FrozenTerminal } from '@/components/terminal-presentation'
import { api, type Api } from '@/lib/api'
import type { TerminalHistoryLine, TerminalHistoryResult } from '@/lib/types'
import { TerminalHistory } from '@/routes/terminal/history'
import { getHistoryCache, type HistoryCache } from '@/routes/terminal/history-cache'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { fakeApi, run } from '@/test/fixtures'

vi.mock('@/components/terminal-presentation', () => ({ captureTerminalPresentation: vi.fn() }))

const frozenScreen: FrozenTerminal = {
  rows: Array.from({ length: 40 }, (_, index) => `<span>original screen ${index}</span>`),
  cols: 80,
  viewportY: 20,
  baseY: 20,
  cellWidth: 8,
  cellHeight: 10,
  fontFamily: 'monospace',
  fontSize: 12,
  letterSpacing: 0,
}

function historyLine(cursor: string, text = cursor): TerminalHistoryLine {
  return { cursor, time: 1, text }
}

function historyResult(lines: TerminalHistoryLine[], over: Partial<TerminalHistoryResult> = {}): TerminalHistoryResult {
  return { lines, has_more: false, ...over }
}

function page(from: number, until: number): TerminalHistoryLine[] {
  return Array.from({ length: until - from }, (_, index) => historyLine(`line-${from + index}`))
}

function firstVisibleRow(element: HTMLElement) {
  const row = [...element.querySelectorAll<HTMLElement>('[data-history-row]')]
    .find((candidate) => Number.parseFloat(candidate.style.top) + Number.parseFloat(candidate.style.height) > element.scrollTop)
  return row ? { text: row.textContent, offset: Number.parseFloat(row.style.top) - element.scrollTop } : null
}

function cacheFor(client: Pick<Api, 'terminalHistory'>, runID = 'run_1') {
  const state = useStore.getState()
  return getHistoryCache({ identityKey: state.identityKey, epoch: state.terminalCacheEpoch, runID, createdAt: state.runs[runID].created_at }, client)
}

function mountHistory(
  cache: HistoryCache,
  alternate = false,
  autoRender = true,
  onReadingChange: (reading: boolean) => void = vi.fn(),
) {
  const host = document.createElement('div')
  const element = document.createElement('div')
  const terminalScreen = document.createElement('div')
  const nativeScrollable = document.createElement('div')
  nativeScrollable.className = 'xterm-scrollable-element'
  const nativeScrollbar = document.createElement('div')
  nativeScrollbar.className = 'scrollbar vertical'
  const nativeSlider = document.createElement('div')
  nativeSlider.className = 'slider'
  nativeScrollbar.append(nativeSlider)
  nativeScrollable.append(nativeScrollbar)
  element.append(nativeScrollable)
  terminalScreen.className = 'xterm-screen'
  terminalScreen.style.width = '640px'
  terminalScreen.style.height = '200px'
  element.append(terminalScreen)
  host.append(element)
  document.body.append(host)
  Object.defineProperty(host, 'clientHeight', { value: 100 })
  const normal = { viewportY: 20, baseY: 20 }
  const alternateBuffer = {}
  const scrollListeners = new Set<(position: number) => void>()
  const renderListeners = new Set<(range: { start: number; end: number }) => void>()
  const renderTerminal = (start = 0, end = 19) => {
    for (const listener of renderListeners) listener({ start, end })
  }
  const terminal = {
    element,
    options: { disableStdin: false },
    cols: 80,
    rows: 20,
    buffer: { active: alternate ? alternateBuffer : normal, normal, alternate: alternateBuffer },
    blur: vi.fn(),
    scrollToBottom: vi.fn(),
    refresh: vi.fn(() => {
      if (autoRender) queueMicrotask(() => renderTerminal())
    }),
    onRender: vi.fn((listener: (range: { start: number; end: number }) => void) => {
      renderListeners.add(listener)
      return { dispose: () => renderListeners.delete(listener) }
    }),
    onScroll: vi.fn((listener: (position: number) => void) => {
      scrollListeners.add(listener)
      return { dispose: () => scrollListeners.delete(listener) }
    }),
  } as unknown as Terminal
  const controller = {
    terminal,
    focusTerminal: vi.fn(),
    setFindOpen: vi.fn(),
    noteViewportInteraction: vi.fn(),
  } as unknown as XtermController
  const beforeDispose = { current: null as ((terminal: Terminal) => void) | null }
  const tools = { current: null as TerminalReadSurface | null }
  const view = render(<TerminalHistory controller={controller} cache={cache} enabled beforeDispose={beforeDispose} tools={tools} onReadingChange={onReadingChange} />)
  return {
    ...view,
    host,
    terminal,
    controller,
    tools,
    onReadingChange,
    nativeSlider,
    renderTerminal,
    scrollNative(row: number) {
      normal.viewportY = row
      for (const listener of scrollListeners) listener(row)
    },
    dispose() {
      act(() => { beforeDispose.current?.(terminal); view.unmount() })
      host.remove()
    },
  }
}

async function beginReading(view: { host: HTMLElement }) {
  await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
  fireEvent.wheel(view.host, { deltaY: -30 })
  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  Object.defineProperties(output, {
    clientHeight: { value: 100 },
    clientWidth: { value: 200 },
    scrollHeight: { get: () => Number.parseFloat((output.firstElementChild as HTMLElement).style.height) },
  })
  fireEvent.scroll(output)
  return output
}

beforeEach(() => {
  const epoch = useStore.getState().terminalCacheEpoch + 1
  useStore.setState({ identityKey: 'history-tests', terminalCacheEpoch: epoch, terminalFontSize: 12, hydrated: true, runs: { run_1: toRecord(run()), run_2: toRecord(run({ id: 'run_2' })) } })
  vi.mocked(captureTerminalPresentation).mockReturnValue(frozenScreen)
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test('posts terminal.history through the RPC endpoint', async () => {
  const signal = new AbortController().signal
  const fetchSpy = vi.fn(async () => ({ ok: true, json: async () => historyResult([historyLine('newest')]) }))
  vi.stubGlobal('fetch', fetchSpy)
  await api.terminalHistory({ run_id: 'run_1', query: 'literal', limit: 12 }, signal)
  expect(fetchSpy).toHaveBeenCalledWith('/api/v1/terminal.history', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ run_id: 'run_1', query: 'literal', limit: 12 }), signal,
  }))
})

test('enters by scrolling without a history button and escapes archive HTML', async () => {
  const client = fakeApi({ terminalHistory: vi.fn(async () => historyResult([historyLine('unsafe', '<img src=x onerror=alert(1)>')])) })
  const view = mountHistory(cacheFor(client))
  const output = await beginReading(view)
  await waitFor(() => expect(client.terminalHistory).toHaveBeenCalledTimes(1))
  fireEvent.scroll(output, { target: { scrollTop: 0 } })
  expect(await screen.findByText('<img src=x onerror=alert(1)>')).toBeDefined()
  expect(output.querySelector('img')).toBeNull()
  expect(output.getAttribute('aria-live')).toBeNull()
  expect(screen.getByRole('separator', { name: 'Recorded output and live screen boundary' })).toBeDefined()
  expect(screen.queryByRole('button', { name: /history|load older/i })).toBeNull()
  fireEvent.keyDown(output, { key: 'End' })
  expect(screen.queryByRole('region', { name: 'Terminal scrollback' })).toBeNull()
  expect(view.terminal.scrollToBottom).toHaveBeenCalled()
  view.dispose()
})

test('accepts the first upward gesture as soon as the restored live surface is announced', async () => {
  const view = mountHistory(cacheFor(fakeApi()), false, true, (reading) => {
    if (!reading) view.host.dispatchEvent(new WheelEvent('wheel', { deltaY: -30, bubbles: true, cancelable: true }))
  })
  expect(await screen.findByRole('region', { name: 'Terminal scrollback' })).toBeDefined()
  view.dispose()
})

test('keeps the latest row, pixel and horizontal anchor when a delayed older page arrives', async () => {
  const older = Promise.withResolvers<TerminalHistoryResult>()
  const client = fakeApi({ terminalHistory: vi.fn()
    .mockResolvedValueOnce(historyResult(page(200, 400), { has_more: true, next_cursor: 'older' }))
    .mockImplementationOnce(() => older.promise) })
  const view = mountHistory(cacheFor(client))
  const output = await beginReading(view)
  await waitFor(() => expect(output.scrollTop).toBe(2188))
  fireEvent.scroll(output, { target: { scrollTop: 103, scrollLeft: 55 } })
  await waitFor(() => expect(client.terminalHistory).toHaveBeenCalledTimes(2))
  fireEvent.scroll(output, { target: { scrollTop: 137, scrollLeft: 71 } })
  const selector = '[data-history-row="-187"]'
  await waitFor(() => expect(output.querySelector(selector)?.textContent).toBe('line-213'))
  const before = Number.parseFloat((output.querySelector(selector) as HTMLElement).style.top) - output.scrollTop
  await act(async () => older.resolve(historyResult(page(0, 200))))
  await waitFor(() => expect(output.scrollTop).toBe(2137))
  expect(output.scrollLeft).toBe(71)
  expect(output.querySelector(selector)?.textContent).toBe('line-213')
  expect(Number.parseFloat((output.querySelector(selector) as HTMLElement).style.top) - output.scrollTop).toBe(before)
  view.dispose()
})

test('keeps the actual partially visible row and exact pixel offset through fractional zoom', async () => {
  vi.mocked(captureTerminalPresentation).mockReturnValue({
    ...frozenScreen,
    cellHeight: 14,
    rows: Array.from({ length: 400 }, (_, index) => `<span>original screen ${index}</span>`),
  })
  const view = mountHistory(cacheFor(fakeApi()))
  const output = await beginReading(view)
  fireEvent.scroll(output, { target: { scrollTop: 4682, scrollLeft: 71 } })
  const pinned = { text: 'original screen 332', offset: -12 }
  expect(firstVisibleRow(output)).toEqual(pinned)

  for (const terminalFontSize of [13, 14, 12]) {
    act(() => useStore.setState({ terminalFontSize }))
    fireEvent.scroll(output)
    expect(firstVisibleRow(output)).toEqual(pinned)
    expect(output.scrollLeft).toBe(71)
  }
  view.dispose()
})

test('retains more than one thousand rows and revisits older and newer pages with bounded DOM', async () => {
  const client = fakeApi({ terminalHistory: vi.fn(async (params) => {
    const end = params.before ? Number(params.before) : 1400
    return historyResult(page(end - 200, end), { has_more: end > 200, next_cursor: end > 200 ? String(end - 200) : undefined })
  }) })
  const cache = cacheFor(client)
  const view = mountHistory(cache)
  const output = await beginReading(view)
  await waitFor(() => expect(cache.snapshot().count).toBe(200))
  for (let count = 400; count <= 1400; count += 200) {
    fireEvent.scroll(output, { target: { scrollTop: 0 } })
    await waitFor(() => expect(cache.snapshot().count).toBe(count))
  }
  fireEvent.scroll(output, { target: { scrollTop: 0 } })
  expect(await screen.findByText('line-0')).toBeDefined()
  fireEvent.scroll(output, { target: { scrollTop: 13950 } })
  expect(await screen.findByText('line-1399')).toBeDefined()
  expect(output.querySelectorAll('[data-history-row]').length).toBeLessThan(50)
  fireEvent.scroll(output, { target: { scrollTop: 0 } })
  expect(await screen.findByText('line-0')).toBeDefined()
  expect(client.terminalHistory).toHaveBeenCalledTimes(7)
  view.dispose()
})

test('restores the saved presentation and exact anchor rather than a newer live screen', async () => {
  const cache = cacheFor(fakeApi({ terminalHistory: vi.fn(async () => historyResult(page(0, 200))) }))
  let view = mountHistory(cache)
  const output = await beginReading(view)
  await waitFor(() => expect(cache.snapshot().count).toBe(200))
  fireEvent.scroll(output, { target: { scrollTop: 2237, scrollLeft: 89 } })
  expect(await screen.findByText('original screen 22')).toBeDefined()
  view.dispose()
  await cache.readView()
  vi.mocked(captureTerminalPresentation).mockReturnValue({ ...frozenScreen, rows: ['<span>new live output</span>'] })
  view = mountHistory(cache)
  const restored = await screen.findByRole('region', { name: 'Terminal scrollback' })
  expect(restored.scrollTop).toBe(2237)
  expect(restored.scrollLeft).toBe(89)
  expect(screen.getByText('original screen 22')).toBeDefined()
  expect(screen.queryByText('new live output')).toBeNull()
  view.dispose()
})

test('searches retained pages outside the mounted window without discarding the presentation', async () => {
  const cache = cacheFor(fakeApi({ terminalHistory: vi.fn(async () => historyResult(page(0, 200))) }))
  const view = mountHistory(cache)
  const output = await beginReading(view)
  await waitFor(() => expect(cache.snapshot().count).toBe(200))
  expect(screen.queryByText('line-13')).toBeNull()
  await act(async () => expect(await view.tools.current?.findNext('line-13')).toBe(true))
  expect(await screen.findByText('line-13')).toBeDefined()
  expect(firstVisibleRow(output)).toEqual({ text: 'line-13', offset: 0 })
  await act(async () => expect(await view.tools.current?.findNext('original screen 20')).toBe(true))
  expect(screen.getByText('original screen 20')).toBeDefined()
  view.dispose()
})

test('leaves alternate-screen wheel and PageUp alone but accepts explicit Shift+PageUp', async () => {
  const client = fakeApi({ terminalHistory: vi.fn(async () => historyResult(page(0, 20))) })
  const view = mountHistory(cacheFor(client), true)
  vi.mocked(captureTerminalPresentation).mockReturnValue({ ...frozenScreen, rows: [], cellHeight: 0, cellWidth: 0, viewportY: 0, baseY: 0 })
  await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
  expect(fireEvent.wheel(view.host, { deltaY: -80 })).toBe(true)
  expect(fireEvent.keyDown(view.host, { key: 'PageUp', code: 'PageUp' })).toBe(true)
  expect(screen.queryByRole('region', { name: 'Terminal scrollback' })).toBeNull()
  fireEvent.keyDown(view.host, { key: 'PageUp', code: 'PageUp', shiftKey: true })
  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  await waitFor(() => expect(client.terminalHistory).toHaveBeenCalledTimes(1))
  fireEvent.scroll(output, { target: { scrollTop: 0 } })
  expect(await screen.findByText('line-0')).toBeDefined()
  view.dispose()
})

test('ignores a late page from the run that was left', async () => {
  const pending = Promise.withResolvers<TerminalHistoryResult>()
  const client = fakeApi({ terminalHistory: vi.fn((params) => params.run_id === 'run_1' ? pending.promise : Promise.resolve(historyResult([historyLine('run-two-output')])) ) })
  const first = mountHistory(cacheFor(client))
  await beginReading(first)
  await waitFor(() => expect(client.terminalHistory).toHaveBeenCalledTimes(1))
  first.dispose()
  const second = mountHistory(cacheFor(client, 'run_2'))
  const output = await beginReading(second)
  await act(async () => pending.resolve(historyResult([historyLine('run-one-private-output')])))
  fireEvent.scroll(output, { target: { scrollTop: 0 } })
  expect(await screen.findByText('run-two-output')).toBeDefined()
  expect(screen.queryByText('run-one-private-output')).toBeNull()
  second.dispose()
})

test('starts a new archive head after returning live while preserving pages during a pinned revisit', async () => {
  const client = fakeApi({ terminalHistory: vi.fn()
    .mockResolvedValueOnce(historyResult([historyLine('old-head')]))
    .mockResolvedValueOnce(historyResult([historyLine('new-head')])) })
  const cache = cacheFor(client)
  const view = mountHistory(cache)
  let output = await beginReading(view)
  await waitFor(() => expect(cache.snapshot().count).toBe(1))
  fireEvent.keyDown(output, { key: 'End' })
  output = await beginReading(view)
  await waitFor(() => expect(client.terminalHistory).toHaveBeenCalledTimes(2))
  fireEvent.scroll(output, { target: { scrollTop: 0 } })
  expect(await screen.findByText('new-head')).toBeDefined()
  expect(screen.queryByText('old-head')).toBeNull()
  view.dispose()
})

test('rebases million-row scroll ranges without changing the visible row or exhausting browser height', async () => {
  const cache: HistoryCache = {
    readView: async () => ({ screen: frozenScreen, anchor: { row: -3_000_000, offset: 3, left: 57 } }),
    saveView: vi.fn(async () => {}),
    snapshot: () => ({ count: 5_000_000, hasMore: false, loading: false, error: null, revision: 1 }),
    subscribe: () => () => {},
    loadOlder: vi.fn(async () => {}),
    resetArchive: vi.fn(async () => {}),
    cancel: vi.fn(),
    readRows: async (start, end) => Array.from({ length: end - start }, (_, offset) => {
      const index = start + offset
      return { index, line: historyLine(String(index), `recorded row ${index}`) }
    }),
  }
  const view = mountHistory(cache)
  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  Object.defineProperties(output, {
    clientHeight: { value: 100 },
    clientWidth: { value: 200 },
    scrollHeight: { get: () => Number.parseFloat((output.firstElementChild as HTMLElement).style.height) },
  })
  fireEvent.scroll(output)
  expect(await screen.findByText('recorded row -3000000')).toBeDefined()
  expect(output.scrollHeight).toBeLessThan(5_000_000)
  expect(output.scrollLeft).toBe(57)
  const first = output.querySelector<HTMLElement>('[data-history-row=\"-3000000\"]')!
  expect(Number.parseFloat(first.style.top) - output.scrollTop).toBe(-3)

  fireEvent.scroll(output, { target: { scrollTop: 31 } })
  await waitFor(() => expect(output.scrollTop).toBe(2_000_011))
  expect(await screen.findByText('recorded row -3199998')).toBeDefined()
  const rebased = output.querySelector<HTMLElement>('[data-history-row=\"-3199998\"]')!
  expect(Number.parseFloat(rebased.style.top) - output.scrollTop).toBe(-3)
  expect(output.scrollLeft).toBe(57)
  expect(output.querySelectorAll('[data-history-row]').length).toBeLessThan(50)

  fireEvent.keyDown(output, { key: 'Home' })
  expect(await screen.findByText('recorded row -5000000')).toBeDefined()
  fireEvent.keyDown(output, { key: 'End' })
  expect(screen.queryByRole('region', { name: 'Terminal scrollback' })).toBeNull()
  view.dispose()
})

test('keeps a failed saved-view read hidden and retries the same retained episode', async () => {
  const client = fakeApi({ terminalHistory: vi.fn(async () => historyResult(page(0, 200))) })
  const cache = cacheFor(client)
  await cache.loadOlder()
  await cache.saveView({ screen: frozenScreen, anchor: { row: -10, offset: 3, left: 71 } })
  vi.spyOn(cache, 'readView').mockRejectedValueOnce(new Error('saved view temporarily unavailable'))
  const view = mountHistory(cache)
  expect(await screen.findByRole('alert', { name: 'Saved terminal view unavailable' })).toBeDefined()
  expect(view.onReadingChange).toHaveBeenLastCalledWith(true)
  fireEvent.wheel(view.host, { deltaY: -30 })
  expect(screen.queryByRole('region', { name: 'Terminal scrollback' })).toBeNull()
  expect(await cache.readView()).toMatchObject({ anchor: { row: -10, offset: 3, left: 71 } })

  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  expect(await screen.findByText('line-190')).toBeDefined()
  expect(firstVisibleRow(output)).toEqual({ text: 'line-190', offset: -3 })
  expect(output.scrollLeft).toBe(71)
  expect(client.terminalHistory).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('alert')).toBeNull()
  view.dispose()
})

test('retries reading the current stored rows after the archive reaches its end', async () => {
  const client = fakeApi({ terminalHistory: vi.fn(async () => historyResult(page(0, 200))) })
  const cache = cacheFor(client)
  await cache.loadOlder()
  await cache.saveView({ screen: frozenScreen, anchor: { row: -10, offset: 3, left: 71 } })
  // Geometry can change the first requested range while the view mounts.
  // Keep storage unavailable for every range until the explicit retry.
  const readRows = vi.spyOn(cache, 'readRows').mockRejectedValue(new Error('stored page temporarily unavailable'))
  const view = mountHistory(cache)
  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  expect(await screen.findByText('Error: stored page temporarily unavailable')).toBeDefined()
  expect(cache.snapshot().hasMore).toBe(false)
  expect(screen.queryByText('line-190')).toBeNull()

  readRows.mockRestore()
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('line-190')).toBeDefined()
  expect(firstVisibleRow(output)).toEqual({ text: 'line-190', offset: -3 })
  expect(output.scrollLeft).toBe(71)
  expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  expect(client.terminalHistory).toHaveBeenCalledTimes(1)
  view.dispose()
})

test('finishes a native scrollbar drag before capturing its final row and horizontal offset', async () => {
  const view = mountHistory(cacheFor(fakeApi()))
  await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
  vi.mocked(captureTerminalPresentation).mockImplementation(() => ({
    ...frozenScreen,
    viewportY: view.terminal.buffer.normal.viewportY,
  }))
  view.host.scrollLeft = 37
  fireEvent.pointerDown(view.nativeSlider, { pointerId: 7 })
  act(() => view.scrollNative(12))
  expect(screen.queryByRole('region', { name: 'Terminal scrollback' })).toBeNull()
  act(() => view.scrollNative(5))
  expect(screen.queryByRole('region', { name: 'Terminal scrollback' })).toBeNull()
  fireEvent.pointerUp(document, { pointerId: 7 })

  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  expect(firstVisibleRow(output)).toEqual({ text: 'original screen 5', offset: 0 })
  expect(output.scrollLeft).toBe(37)
  expect(screen.getByText('original screen 5')).toBeDefined()
  view.dispose()
})

test('coalesces first-entry scroll intent and waits for the complete rendered screen', async () => {
  const view = mountHistory(cacheFor(fakeApi()), false, false)
  await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
  view.host.scrollLeft = 29
  fireEvent.wheel(view.host, { deltaY: -30 })
  fireEvent.wheel(view.host, { deltaY: -20 })
  act(() => view.renderTerminal(4, 8))
  expect(screen.queryByRole('region', { name: 'Terminal scrollback' })).toBeNull()
  vi.mocked(captureTerminalPresentation).mockReturnValue({
    ...frozenScreen,
    rows: frozenScreen.rows.map((_, index) => `<span>committed screen ${index}</span>`),
  })
  act(() => view.renderTerminal())
  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  expect(firstVisibleRow(output)).toEqual({ text: 'committed screen 15', offset: 0 })
  expect(output.scrollLeft).toBe(29)
  expect(screen.getByText('committed screen 16')).toBeDefined()
  expect(screen.queryByText('original screen 16')).toBeNull()
  view.dispose()
})

test('saves pending navigation when disposal precedes paint without accepting a late render', async () => {
  const cache = cacheFor(fakeApi())
  const view = mountHistory(cache, false, false)
  await waitFor(() => expect(screen.queryByRole('status', { name: 'Restoring saved terminal view' })).toBeNull())
  view.host.scrollLeft = 47
  fireEvent.wheel(view.host, { deltaY: -23 })
  fireEvent.wheel(view.host, { deltaY: -14 })
  view.dispose()
  expect(await cache.readView()).toMatchObject({ anchor: { row: 16, offset: 3, left: 47 } })
  vi.mocked(captureTerminalPresentation).mockReturnValue({ ...frozenScreen, rows: ['<span>late paint</span>'] })
  act(() => view.renderTerminal())
  const restored = mountHistory(cache)
  const output = await screen.findByRole('region', { name: 'Terminal scrollback' })
  expect(firstVisibleRow(output)).toEqual({ text: 'original screen 16', offset: -3 })
  expect(output.scrollLeft).toBe(47)
  expect(screen.getByText('original screen 16')).toBeDefined()
  expect(screen.queryByText('late paint')).toBeNull()
  restored.dispose()
})

test('keeps native copy available and uses the terminal clipboard fallback for explicit copy', async () => {
  const view = mountHistory(cacheFor(fakeApi()))
  const output = await beginReading(view)
  const row = screen.getByText('original screen 20')
  const range = document.createRange()
  range.selectNodeContents(row)
  const selection = window.getSelection()!
  selection.removeAllRanges()
  selection.addRange(range)
  expect(fireEvent.keyDown(output, { ctrlKey: true, code: 'KeyC', key: 'c' })).toBe(true)

  let copied = ''
  const originalExecCommand = Object.getOwnPropertyDescriptor(document, 'execCommand')
  Object.defineProperty(document, 'execCommand', { configurable: true, value: (command: string) => {
    if (command !== 'copy') return false
    copied = document.querySelector<HTMLTextAreaElement>('textarea[readonly]')?.value ?? ''
    return true
  } })
  vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
  try {
    fireEvent.keyDown(output, { ctrlKey: true, shiftKey: true, code: 'KeyC', key: 'C' })
    await waitFor(() => expect(copied).toBe('original screen 20'))
  } finally {
    if (originalExecCommand) Object.defineProperty(document, 'execCommand', originalExecCommand)
    else Reflect.deleteProperty(document, 'execCommand')
    selection.removeAllRanges()
    view.dispose()
  }
})

test('does not steal a newer focus intent or focus a disposed view after returning live', async () => {
  const frames: FrameRequestCallback[] = []
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => frames.push(callback))
  vi.stubGlobal('cancelAnimationFrame', vi.fn())
  const view = mountHistory(cacheFor(fakeApi()))
  const output = await beginReading(view)
  fireEvent.keyDown(output, { key: 'End' })
  const otherInput = document.createElement('input')
  document.body.append(otherInput)
  otherInput.focus()
  act(() => { for (const callback of frames.splice(0)) callback(0) })
  expect(document.activeElement).toBe(otherInput)
  expect(view.controller.focusTerminal).not.toHaveBeenCalled()

  const next = await beginReading(view)
  fireEvent.keyDown(next, { key: 'End' })
  view.dispose()
  act(() => { for (const callback of frames.splice(0)) callback(0) })
  expect(view.controller.focusTerminal).not.toHaveBeenCalled()
  otherInput.remove()
})
