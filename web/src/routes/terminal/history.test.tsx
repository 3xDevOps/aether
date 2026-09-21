import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { api } from '@/lib/api'
import type { TerminalHistoryLine, TerminalHistoryResult } from '@/lib/types'
import { TerminalHistory } from '@/routes/terminal/history'
import { fakeApi } from '@/test/fixtures'

function historyLine(cursor: string, text = cursor): TerminalHistoryLine {
  return { cursor, time: 1, text }
}

function historyResult(
  lines: TerminalHistoryLine[],
  over: Partial<TerminalHistoryResult> = {},
): TerminalHistoryResult {
  return { lines, has_more: false, ...over }
}

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test('posts terminal.history through the RPC endpoint', async () => {
  const signal = new AbortController().signal
  const fetchSpy = vi.fn(async () => ({
    ok: true,
    json: async () => historyResult([historyLine('newest')]),
  }))
  vi.stubGlobal('fetch', fetchSpy)

  await api.terminalHistory({ run_id: 'run_1', query: 'literal', limit: 12 }, signal)

  expect(fetchSpy).toHaveBeenCalledWith('/api/v1/terminal.history', expect.objectContaining({
    method: 'POST',
    body: JSON.stringify({ run_id: 'run_1', query: 'literal', limit: 12 }),
    signal,
  }))
})

test('opens with the newest page and renders history as plain text', async () => {
  const client = fakeApi({
    terminalHistory: vi.fn(async () =>
      historyResult([historyLine('cursor-1', '<strong>recorded output</strong>')]),
    ),
  })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))

  expect(await dialog.findByText('<strong>recorded output</strong>')).toBeDefined()
  expect(dialog.queryByRole('strong')).toBeNull()
  expect(client.terminalHistory).toHaveBeenCalledWith(
    { run_id: 'run_1', limit: 200 },
    expect.any(AbortSignal),
  )
})

test('makes terminal history output keyboard-focusable', async () => {
  const client = fakeApi({
    terminalHistory: vi.fn(async () => historyResult([historyLine('cursor-1', 'focusable output')])),
  })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const output = await screen.findByLabelText('Terminal history output')

  expect(output.tabIndex).toBe(0)
  output.focus()
  expect(document.activeElement).toBe(output)
})

test('announces request status without placing transcript output in live regions', async () => {
  const initialPage = Promise.withResolvers<TerminalHistoryResult>()
  const olderPage = Promise.withResolvers<TerminalHistoryResult>()
  const searchPage = Promise.withResolvers<TerminalHistoryResult>()
  const terminalHistory = vi.fn(async (params: { before?: string; query?: string }) => {
    if (params.query) return searchPage.promise
    if (params.before) return olderPage.promise
    return initialPage.promise
  })
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  const status = dialog.getByRole('status')
  const expectTranscriptOutsideLiveRegions = (text: string) => {
    const output = dialog.getByLabelText('Terminal history output')
    expect(output.textContent).toBe(text)
    expect(status.textContent).not.toContain(text)
    expect(
      output.closest(
        '[aria-live]:not([aria-live="off"]), [role="status"], [role="alert"], [role="log"]',
      ),
    ).toBeNull()
  }

  expect(status.getAttribute('aria-live')).toBe('polite')
  expect(status.getAttribute('aria-atomic')).toBe('true')
  expect(status.textContent).toBe('Loading terminal history…')
  expect(dialog.queryByLabelText('Terminal history output')).toBeNull()

  await act(async () => initialPage.resolve(
    historyResult([historyLine('newest', 'new transcript line')], {
      has_more: true,
      next_cursor: 'older-page',
    }),
  ))
  expect(status.textContent).toBe('Loaded 1 line. Older history is available.')
  expectTranscriptOutsideLiveRegions('new transcript line')

  fireEvent.click(dialog.getByRole('button', { name: 'Load older history' }))
  expect(status.textContent).toBe('Loading older history…')
  expectTranscriptOutsideLiveRegions('new transcript line')

  await act(async () => olderPage.resolve(
    historyResult([historyLine('oldest', 'old transcript line')]),
  ))
  expect(status.textContent).toBe('Loaded 1 line.')
  expectTranscriptOutsideLiveRegions('old transcript line\nnew transcript line')

  vi.useFakeTimers()
  fireEvent.change(dialog.getByLabelText('Search terminal history'), {
    target: { value: 'needle' },
  })
  expect(status.textContent).toBe('Loading terminal history…')
  expect(dialog.queryByLabelText('Terminal history output')).toBeNull()
  await act(async () => vi.advanceTimersByTimeAsync(300))
  await act(async () => searchPage.resolve(
    historyResult([historyLine('match', 'matching transcript line')]),
  ))

  expect(status.textContent).toBe('Loaded 1 matching line.')
  expectTranscriptOutsideLiveRegions('matching transcript line')
})

test('shows a partial scan instead of no results when an empty search page has more history', async () => {
  const terminalHistory = vi
    .fn()
    .mockResolvedValueOnce(historyResult([historyLine('initial')]))
    .mockResolvedValueOnce(
      historyResult([], { has_more: true, next_cursor: 'older-search-window' }),
    )
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(await dialog.findByText('initial')).toBeDefined()

  vi.useFakeTimers()
  fireEvent.change(dialog.getByLabelText('Search terminal history'), {
    target: { value: 'missing' },
  })
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(
    dialog.getByText('No matches in this partial scan. Load older history to continue searching.'),
  ).toBeDefined()
  expect(dialog.queryByText('No matching terminal history.')).toBeNull()
  expect(dialog.getByRole('button', { name: 'Load older history' })).toBeDefined()
  expect(dialog.getByRole('status').textContent).toBe(
    'Loaded 0 matching lines. Older history is available.',
  )
})

test('shows true no-results copy when an empty search has exhausted history', async () => {
  const terminalHistory = vi
    .fn()
    .mockResolvedValueOnce(historyResult([historyLine('initial')]))
    .mockResolvedValueOnce(historyResult([]))
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(await dialog.findByText('initial')).toBeDefined()

  vi.useFakeTimers()
  fireEvent.change(dialog.getByLabelText('Search terminal history'), {
    target: { value: 'missing' },
  })
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(dialog.getByText('No matching terminal history.')).toBeDefined()
  expect(dialog.queryByText(/partial scan/i)).toBeNull()
  expect(dialog.queryByRole('button', { name: 'Load older history' })).toBeNull()
  expect(dialog.getByRole('status').textContent).toBe('Loaded 0 matching lines.')
})

test('prepends older pages while preserving chronological display', async () => {
  const terminalHistory = vi
    .fn()
    .mockResolvedValueOnce(
      historyResult([historyLine('new-1'), historyLine('new-2')], {
        has_more: true,
        next_cursor: 'opaque older cursor',
      }),
    )
    .mockResolvedValueOnce(historyResult([historyLine('old-1'), historyLine('old-2')]))
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(await dialog.findByRole('button', { name: 'Load older history' }))

  await waitFor(() => expect(terminalHistory).toHaveBeenCalledTimes(2))
  expect(terminalHistory).toHaveBeenLastCalledWith(
    { run_id: 'run_1', before: 'opaque older cursor', limit: 200 },
    expect.any(AbortSignal),
  )
  await waitFor(() => {
    expect(dialog.getByLabelText('Terminal history output').textContent).toBe(
      'old-1\nold-2\nnew-1\nnew-2',
    )
  })
})

test('keeps the paging control focused while loading consecutive pages', async () => {
  const user = userEvent.setup()
  const firstOlderPage = Promise.withResolvers<TerminalHistoryResult>()
  const exhaustedPage = Promise.withResolvers<TerminalHistoryResult>()
  const terminalHistory = vi.fn(async ({ before }: { before?: string }) => {
    if (before === 'first-older-page') return firstOlderPage.promise
    if (before === 'exhausted-page') return exhaustedPage.promise
    return historyResult([historyLine('newest')], {
      has_more: true,
      next_cursor: 'first-older-page',
    })
  })
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  const paging = await dialog.findByRole('button', { name: 'Load older history' })
  paging.focus()
  await user.keyboard('{Enter}')

  expect(dialog.getByRole('button', { name: 'Loading older history…' })).toBe(paging)
  expect(paging.getAttribute('aria-disabled')).toBe('true')
  expect(paging.getAttribute('aria-busy')).toBe('true')
  expect(paging.tabIndex).toBe(0)
  expect(document.activeElement).toBe(paging)
  await user.keyboard('{Enter}')
  expect(terminalHistory).toHaveBeenCalledTimes(2)

  await act(async () => firstOlderPage.resolve(
    historyResult([historyLine('older')], {
      has_more: true,
      next_cursor: 'exhausted-page',
    }),
  ))
  expect(dialog.getByRole('button', { name: 'Load older history' })).toBe(paging)
  expect(paging.getAttribute('aria-disabled')).toBe('false')
  expect(paging.getAttribute('aria-busy')).toBe('false')
  expect(document.activeElement).toBe(paging)

  await user.keyboard('{Enter}')
  expect(dialog.getByRole('button', { name: 'Loading older history…' })).toBe(paging)
  expect(document.activeElement).toBe(paging)
  expect(terminalHistory).toHaveBeenCalledTimes(3)

  await act(async () => exhaustedPage.resolve(historyResult([historyLine('oldest')])))
  await waitFor(() => {
    expect(dialog.queryByRole('button', { name: /older history/i })).toBeNull()
    expect(document.activeElement).toBe(dialog.getByLabelText('Terminal history output'))
  })
})

test('accepts a 256-byte ASCII query and blocks a longer query without retrying', async () => {
  const terminalHistory = vi.fn(async () => historyResult([historyLine('result')]))
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(await dialog.findByText('result')).toBeDefined()

  vi.useFakeTimers()
  const search = dialog.getByLabelText('Search terminal history')
  const maxQuery = 'a'.repeat(256)
  fireEvent.change(search, { target: { value: maxQuery } })
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(terminalHistory).toHaveBeenLastCalledWith(
    { run_id: 'run_1', query: maxQuery, limit: 200 },
    expect.any(AbortSignal),
  )

  fireEvent.change(search, { target: { value: `${maxQuery}a` } })
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(terminalHistory).toHaveBeenCalledTimes(2)
  const validationAlert = dialog.getByRole('alert')
  expect(validationAlert.textContent).toBe(
    'Search query exceeds 256 UTF-8 bytes. Shorten it to search.',
  )
  expect(search.getAttribute('aria-describedby')).toBe(validationAlert.id)
  expect(dialog.getByRole('status').textContent).toBe('')
  expect(dialog.queryByRole('button', { name: 'Retry' })).toBeNull()
})

test('counts multibyte emoji by UTF-8 bytes at the query boundary', async () => {
  const terminalHistory = vi.fn(async () => historyResult([historyLine('result')]))
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(await dialog.findByText('result')).toBeDefined()

  vi.useFakeTimers()
  const search = dialog.getByLabelText('Search terminal history')
  const maxQuery = '😀'.repeat(64)
  fireEvent.change(search, { target: { value: maxQuery } })
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(terminalHistory).toHaveBeenLastCalledWith(
    { run_id: 'run_1', query: maxQuery, limit: 200 },
    expect.any(AbortSignal),
  )

  fireEvent.change(search, { target: { value: `${maxQuery}é` } })
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(terminalHistory).toHaveBeenCalledTimes(2)
  expect(dialog.getByRole('alert').textContent).toContain('exceeds 256 UTF-8 bytes')
  expect(dialog.getByRole('status').textContent).toBe('')
})

test('debounces search and ignores a stale result', async () => {
  const stale = Promise.withResolvers<TerminalHistoryResult>()
  const terminalHistory = vi.fn(async (params: { query?: string }) => {
    if (params.query === 'first') return stale.promise
    if (params.query === 'second') return historyResult([historyLine('second-result')])
    return historyResult([historyLine('initial-result')])
  })
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(await dialog.findByText('initial-result')).toBeDefined()

  vi.useFakeTimers()
  const search = dialog.getByLabelText('Search terminal history')
  fireEvent.change(search, { target: { value: 'first' } })
  await act(async () => vi.advanceTimersByTimeAsync(300))
  expect(terminalHistory).toHaveBeenLastCalledWith(
    { run_id: 'run_1', query: 'first', limit: 200 },
    expect.any(AbortSignal),
  )

  fireEvent.change(search, { target: { value: 'second' } })
  await act(async () => vi.advanceTimersByTimeAsync(299))
  expect(terminalHistory).toHaveBeenCalledTimes(2)
  await act(async () => vi.advanceTimersByTimeAsync(1))
  expect(dialog.getByText('second-result')).toBeDefined()

  await act(async () => stale.resolve(historyResult([historyLine('stale-result')])))
  expect(dialog.queryByText('stale-result')).toBeNull()
})

test('keeps the chronologically oldest one thousand lines after prepending', async () => {
  const newest = Array.from({ length: 800 }, (_, index) => historyLine(`new-${index}`))
  const older = Array.from({ length: 400 }, (_, index) => historyLine(`old-${index}`))
  const client = fakeApi({
    terminalHistory: vi
      .fn()
      .mockResolvedValueOnce(historyResult(newest, { has_more: true, next_cursor: 'older' }))
      .mockResolvedValueOnce(historyResult(older)),
  })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(await dialog.findByRole('button', { name: 'Load older history' }))

  await waitFor(() => {
    const output = dialog.getByLabelText('Terminal history output')
    const renderedLines = output.querySelectorAll('[data-history-line]')
    expect(renderedLines).toHaveLength(1000)
    expect(renderedLines[0]?.firstChild?.textContent).toBe('old-0')
    expect(renderedLines[999]?.firstChild?.textContent).toBe('new-599')
    expect(output.textContent).toMatch(/^old-0\nold-1\n/)
    expect(output.textContent).toMatch(/new-598\nnew-599$/)
  })
})

test('shows a retryable error and retries the failed page', async () => {
  const recovery = Promise.withResolvers<TerminalHistoryResult>()
  const terminalHistory = vi
    .fn()
    .mockRejectedValueOnce(new Error('history is temporarily unavailable'))
    .mockReturnValueOnce(recovery.promise)
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect((await dialog.findByRole('alert')).textContent).toContain('history is temporarily unavailable')

  fireEvent.click(dialog.getByRole('button', { name: 'Retry' }))
  expect(dialog.queryByRole('alert')).toBeNull()
  expect(dialog.getByRole('status').textContent).toBe('Loading terminal history…')
  expect(dialog.queryByRole('button', { name: 'Retry' })).toBeNull()

  await act(async () => recovery.resolve(historyResult([historyLine('recovered')])))
  expect(await dialog.findByText('recovered')).toBeDefined()
  expect(terminalHistory).toHaveBeenCalledTimes(2)
})

test('cancels an in-flight page on close and ignores it after reopening', async () => {
  const stale = Promise.withResolvers<TerminalHistoryResult>()
  let staleSignal: AbortSignal | undefined
  const terminalHistory = vi
    .fn()
    .mockImplementationOnce((_params, signal) => {
      staleSignal = signal
      return stale.promise
    })
    .mockResolvedValueOnce(historyResult([historyLine('fresh-after-reopen')]))
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const firstDialog = within(screen.getByRole('dialog'))
  expect(firstDialog.getByRole('status').textContent).toBe('Loading terminal history…')

  fireEvent.click(firstDialog.getByRole('button', { name: 'Close' }))
  expect(staleSignal?.aborted).toBe(true)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const reopenedDialog = within(screen.getByRole('dialog'))
  expect(await reopenedDialog.findByText('fresh-after-reopen')).toBeDefined()

  await act(async () => stale.resolve(historyResult([historyLine('stale-before-close')])))
  expect(reopenedDialog.queryByText('stale-before-close')).toBeNull()
  expect(reopenedDialog.getByText('fresh-after-reopen')).toBeDefined()
})

test('cancels a pending search when closed before the debounce elapses', async () => {
  const terminalHistory = vi.fn(async () => historyResult([historyLine('newest')]))
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(await dialog.findByText('newest')).toBeDefined()

  vi.useFakeTimers()
  fireEvent.change(dialog.getByLabelText('Search terminal history'), {
    target: { value: 'must-not-run' },
  })
  fireEvent.click(dialog.getByRole('button', { name: 'Close' }))
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(terminalHistory).toHaveBeenCalledTimes(1)
})

test('cancels a pending debounce so only the latest query is requested', async () => {
  const terminalHistory = vi.fn(async ({ query }: { query?: string }) =>
    historyResult([historyLine(query ?? 'initial')]),
  )
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(await dialog.findByText('initial')).toBeDefined()

  vi.useFakeTimers()
  const search = dialog.getByLabelText('Search terminal history')
  fireEvent.change(search, { target: { value: 'superseded-before-request' } })
  await act(async () => vi.advanceTimersByTimeAsync(299))
  fireEvent.change(search, { target: { value: 'latest-query' } })
  await act(async () => vi.advanceTimersByTimeAsync(300))

  expect(terminalHistory).toHaveBeenCalledTimes(2)
  expect(terminalHistory).toHaveBeenLastCalledWith(
    { run_id: 'run_1', query: 'latest-query', limit: 200 },
    expect.any(AbortSignal),
  )
  expect(dialog.getByText('latest-query')).toBeDefined()
})

test('ignores an older-page response superseded by a new search', async () => {
  const staleOlder = Promise.withResolvers<TerminalHistoryResult>()
  let olderSignal: AbortSignal | undefined
  const terminalHistory = vi.fn(
    async (params: { before?: string; query?: string }, signal?: AbortSignal) => {
      if (params.before) {
        olderSignal = signal
        return staleOlder.promise
      }
      if (params.query) return historyResult([historyLine('search-result')])
      return historyResult([historyLine('newest')], {
        has_more: true,
        next_cursor: 'older-page',
      })
    },
  )
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(await dialog.findByRole('button', { name: 'Load older history' }))

  vi.useFakeTimers()
  fireEvent.change(dialog.getByLabelText('Search terminal history'), {
    target: { value: 'new query' },
  })
  expect(olderSignal?.aborted).toBe(true)
  await act(async () => vi.advanceTimersByTimeAsync(300))
  expect(dialog.getByText('search-result')).toBeDefined()

  await act(async () => staleOlder.resolve(historyResult([historyLine('stale-older')])))
  expect(dialog.queryByText('stale-older')).toBeNull()
  expect(dialog.getByLabelText('Terminal history output').textContent).toBe('search-result')
})

test('deduplicates overlapping cursors and stops a repeated continuation cursor', async () => {
  const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  const terminalHistory = vi
    .fn()
    .mockResolvedValueOnce(
      historyResult(
        [
          historyLine('new-1'),
          historyLine('new-1', 'duplicate newest'),
          historyLine('new-2'),
        ],
        { has_more: true, next_cursor: 'older-page' },
      ),
    )
    .mockResolvedValueOnce(
      historyResult(
        [
          historyLine('old-1'),
          historyLine('old-1', 'duplicate older'),
          historyLine('new-1'),
        ],
        { has_more: true, next_cursor: 'older-page' },
      ),
    )
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(await dialog.findByRole('button', { name: 'Load older history' }))

  await waitFor(() => {
    const output = dialog.getByLabelText('Terminal history output')
    expect(output.textContent).toBe('old-1\nnew-1\nnew-2')
    expect(output.querySelectorAll('[data-history-line]')).toHaveLength(3)
  })
  expect(dialog.queryByRole('button', { name: 'Load older history' })).toBeNull()
  expect(
    consoleError.mock.calls.some((call) => call.join(' ').includes('same key')),
  ).toBe(false)
})

test('keeps paging when an empty page repeats its cutoff cursor', async () => {
  const terminalHistory = vi
    .fn()
    .mockResolvedValueOnce(
      historyResult([historyLine('newest')], { has_more: true, next_cursor: 'cutoff' }),
    )
    .mockResolvedValueOnce(historyResult([], { has_more: true, next_cursor: 'cutoff' }))
    .mockResolvedValueOnce(
      historyResult([historyLine('older')], { has_more: false, next_cursor: '' }),
    )
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  fireEvent.click(await dialog.findByRole('button', { name: 'Load older history' }))
  expect(await dialog.findByText('Loaded 0 lines. Older history is available.')).toBeDefined()
  fireEvent.click(dialog.getByRole('button', { name: 'Load older history' }))
  await waitFor(() => {
    expect(dialog.getByLabelText('Terminal history output').textContent).toContain('older')
  })
  expect(terminalHistory).toHaveBeenCalledTimes(3)
})

test('preserves focus and the transcript while retrying a failed older page', async () => {
  const user = userEvent.setup()
  const recovery = Promise.withResolvers<TerminalHistoryResult>()
  const terminalHistory = vi
    .fn()
    .mockResolvedValueOnce(
      historyResult([historyLine('newest')], {
        has_more: true,
        next_cursor: 'older-page',
      }),
    )
    .mockRejectedValueOnce(new Error('older page failed'))
    .mockReturnValueOnce(recovery.promise)
  const client = fakeApi({ terminalHistory })
  render(<TerminalHistory runID="run_1" client={client} />)

  fireEvent.click(screen.getByRole('button', { name: 'Open terminal history' }))
  const dialog = within(screen.getByRole('dialog'))
  const paging = await dialog.findByRole('button', { name: 'Load older history' })
  paging.focus()
  await user.keyboard('{Enter}')

  expect((await dialog.findByRole('alert')).textContent).toContain('older page failed')
  expect(dialog.getByLabelText('Terminal history output').textContent).toBe('newest')
  const retry = dialog.getByRole('button', { name: 'Retry' })
  expect(document.activeElement).toBe(retry)

  await user.keyboard('{Enter}')
  const retryingPaging = dialog.getByRole('button', { name: 'Loading older history…' })
  expect(document.activeElement).toBe(retryingPaging)
  expect(retryingPaging.getAttribute('aria-disabled')).toBe('true')
  expect(retryingPaging.getAttribute('aria-busy')).toBe('true')
  await user.keyboard('{Enter}')
  expect(terminalHistory).toHaveBeenCalledTimes(3)

  await act(async () => recovery.resolve(
    historyResult([historyLine('oldest')], {
      has_more: true,
      next_cursor: 'another-older-page',
    }),
  ))
  await waitFor(() => {
    expect(dialog.getByLabelText('Terminal history output').textContent).toBe(
      'oldest\nnewest',
    )
    expect(dialog.getByRole('button', { name: 'Load older history' })).toBe(retryingPaging)
    expect(document.activeElement).toBe(retryingPaging)
  })
  expect(terminalHistory).toHaveBeenLastCalledWith(
    { run_id: 'run_1', before: 'older-page', limit: 200 },
    expect.any(AbortSignal),
  )
})