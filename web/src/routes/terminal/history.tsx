import { useEffect, useId, useRef, useState } from 'react'
import { BookOpenText } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import type { TerminalHistoryLine } from '@/lib/types'

const pageSize = 200
const maxCachedLines = 1000
const searchDelayMs = 300
const maxQueryBytes = 256
const utf8Encoder = new TextEncoder()

type PageRequest = {
  query: string
  before?: string
  replace: boolean
}

type TerminalHistoryProps = {
  runID: string
  client?: Pick<Api, 'terminalHistory'>
}

function deduplicateLines(lines: TerminalHistoryLine[]) {
  const cursors = new Set<string>()
  return lines.filter((line) => {
    if (cursors.has(line.cursor)) return false
    cursors.add(line.cursor)
    return true
  })
}

export function TerminalHistory({ runID, client = api }: TerminalHistoryProps) {
  const searchID = useId()
  const statusID = useId()
  const validationErrorID = useId()
  const searchInput = useRef<HTMLInputElement>(null)
  const historyOutput = useRef<HTMLPreElement>(null)
  const loadOlderButton = useRef<HTMLButtonElement>(null)
  const retryButton = useRef<HTMLButtonElement>(null)
  const pendingPagingFocus = useRef<'output' | 'paging' | 'retry' | null>(null)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [lines, setLines] = useState<TerminalHistoryLine[]>([])
  const [nextCursor, setNextCursor] = useState<string>()
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [queryValidationError, setQueryValidationError] = useState<string | null>(null)
  const [statusMessage, setStatusMessage] = useState('')
  const activeQuery = useRef('')
  const requestSequence = useRef(0)
  const requestController = useRef<AbortController | null>(null)
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const retryRequest = useRef<PageRequest | null>(null)
  const cachedLines = useRef<TerminalHistoryLine[]>([])
  const usedContinuationCursors = useRef(new Set<string>())

  const cancelPending = () => {
    requestSequence.current += 1
    requestController.current?.abort()
    requestController.current = null
    if (searchTimer.current !== null) {
      clearTimeout(searchTimer.current)
      searchTimer.current = null
    }
  }

  const loadPage = async ({ query: requestedQuery, before, replace }: PageRequest) => {
    requestController.current?.abort()
    const controller = new AbortController()
    requestController.current = controller
    const sequence = ++requestSequence.current
    activeQuery.current = requestedQuery
    retryRequest.current = null
    setLoading(true)
    setError(null)
    setStatusMessage(before ? 'Loading older history…' : 'Loading terminal history…')

    try {
      const page = await client.terminalHistory(
        {
          run_id: runID,
          ...(before ? { before } : {}),
          ...(requestedQuery ? { query: requestedQuery } : {}),
          limit: pageSize,
        },
        controller.signal,
      )
      if (sequence !== requestSequence.current) return

      const pageLines = deduplicateLines(page.lines)
      let loadedCount: number

      if (replace) {
        cachedLines.current = pageLines.slice(-maxCachedLines)
        loadedCount = cachedLines.current.length
      } else {
        const cachedCursors = new Set(cachedLines.current.map((line) => line.cursor))
        const olderLines = pageLines.filter((line) => !cachedCursors.has(line.cursor))
        cachedLines.current = [...olderLines, ...cachedLines.current].slice(0, maxCachedLines)
        loadedCount = olderLines.length
        if (before) usedContinuationCursors.current.add(before)
      }
      setLines(cachedLines.current)

      const continuationCursor = page.next_cursor
      // A timed-out page returns the same cursor and no lines. That is a
      // retry, not the end of the archive. A repeated cursor that already
      // delivered lines would loop, so that one still stops.
      const repeatedCutoff = Boolean(before) && continuationCursor === before
      const canLoadMore =
        page.has_more &&
        typeof continuationCursor === 'string' &&
        continuationCursor.length > 0 &&
        (repeatedCutoff
          ? loadedCount === 0
          : !usedContinuationCursors.current.has(continuationCursor))
      if (
        before &&
        !canLoadMore &&
        document.activeElement === loadOlderButton.current
      ) {
        pendingPagingFocus.current = 'output'
      }
      setNextCursor(canLoadMore ? continuationCursor : undefined)
      setHasMore(canLoadMore)

      const lineLabel = `${requestedQuery ? 'matching ' : ''}${
        loadedCount === 1 ? 'line' : 'lines'
      }`
      setStatusMessage(
        `Loaded ${loadedCount} ${lineLabel}.${canLoadMore ? ' Older history is available.' : ''}`,
      )
    } catch (cause) {
      if (sequence !== requestSequence.current || controller.signal.aborted) return
      retryRequest.current = { query: requestedQuery, before, replace }
      if (before && document.activeElement === loadOlderButton.current) {
        pendingPagingFocus.current = 'retry'
      }
      setError(cause instanceof Error ? cause.message : 'Terminal history could not be loaded.')
      setStatusMessage('')
    } finally {
      if (sequence === requestSequence.current) {
        requestController.current = null
        setLoading(false)
      }
    }
  }

  const changeOpen = (nextOpen: boolean) => {
    setOpen(nextOpen)
    cancelPending()
    pendingPagingFocus.current = null
    if (!nextOpen) return

    setQuery('')
    cachedLines.current = []
    usedContinuationCursors.current.clear()
    setLines([])
    setNextCursor(undefined)
    setHasMore(false)
    setError(null)
    setQueryValidationError(null)
    activeQuery.current = ''
    void loadPage({ query: '', replace: true })
  }

  const changeQuery = (value: string) => {
    setQuery(value)
    cancelPending()
    pendingPagingFocus.current = null
    retryRequest.current = null
    activeQuery.current = value
    cachedLines.current = []
    usedContinuationCursors.current.clear()
    setLines([])
    setNextCursor(undefined)
    setHasMore(false)
    setError(null)
    if (utf8Encoder.encode(value).byteLength > maxQueryBytes) {
      const message = `Search query exceeds ${maxQueryBytes} UTF-8 bytes. Shorten it to search.`
      setQueryValidationError(message)
      setLoading(false)
      setStatusMessage('')
      return
    }

    setQueryValidationError(null)
    setLoading(true)
    setStatusMessage('Loading terminal history…')
    searchTimer.current = setTimeout(() => {
      searchTimer.current = null
      void loadPage({ query: value, replace: true })
    }, searchDelayMs)
  }

  const loadOlder = () => {
    if (loading || requestController.current !== null || !hasMore || !nextCursor) return
    void loadPage({ query: activeQuery.current, before: nextCursor, replace: false })
  }

  const retry = () => {
    const request = retryRequest.current
    if (loading || requestController.current !== null || request === null) return
    if (request.before && document.activeElement === retryButton.current) {
      pendingPagingFocus.current = 'paging'
    }
    void loadPage(request)
  }

  useEffect(() => () => cancelPending(), [])
  useEffect(() => {
    const focusTarget = pendingPagingFocus.current
    if (focusTarget === 'paging' && loading && !error && hasMore) {
      pendingPagingFocus.current = null
      loadOlderButton.current?.focus()
    } else if (focusTarget === 'retry' && !loading && error) {
      pendingPagingFocus.current = null
      retryButton.current?.focus()
    } else if (focusTarget === 'output' && !loading && !error && !hasMore) {
      pendingPagingFocus.current = null
      const target = historyOutput.current ?? searchInput.current
      target?.focus()
    }
  }, [error, hasMore, lines, loading])

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px]">
        <DialogTrigger asChild>
          <Button type="button" size="sm" variant="outline">
            <BookOpenText aria-hidden size={14} />
            Open terminal history
          </Button>
        </DialogTrigger>
        <span className="min-w-0 flex-[1_1_18rem] text-muted-foreground">
          Browse older recorded output without expanding live terminal scrollback.
        </span>
      </div>

      <DialogContent className="flex max-h-[calc(100dvh-2rem-var(--safe-top))] min-h-0 flex-col sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle>Terminal history</DialogTitle>
          <DialogDescription>
            Browse recorded output in bounded pages. Search matches literal text on the server.
          </DialogDescription>
        </DialogHeader>

        <label className="grid gap-1 text-[13px] font-medium" htmlFor={searchID}>
          Search terminal history
          <input
            ref={searchInput}
            id={searchID}
            type="search"
            value={query}
            onChange={(event) => changeQuery(event.target.value)}
            aria-invalid={queryValidationError !== null}
            aria-describedby={queryValidationError ? validationErrorID : undefined}
            placeholder="Search recorded output"
            className="h-9 rounded-[3px] border border-input bg-background px-3 font-normal outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30"
          />
        </label>

        <div className="flex min-h-0 flex-1 flex-col gap-2">
          <p
            id={statusID}
            role="status"
            aria-live="polite"
            aria-atomic="true"
            className="m-0 text-[13px] text-muted-foreground"
          >
            {statusMessage}
          </p>

          {queryValidationError && (
            <p
              id={validationErrorID}
              role="alert"
              className="m-0 text-[13px] text-[var(--danger-soft-foreground)]"
            >
              {queryValidationError}
            </p>
          )}

          {error && (
            <div role="alert" className="flex flex-wrap items-center gap-2 text-[13px] text-[var(--danger-soft-foreground)]">
              <span className="min-w-0 flex-1 break-words">{error}</span>
              <Button
                ref={retryButton}
                type="button"
                size="sm"
                variant="outline"
                onClick={retry}
              >
                Retry
              </Button>
            </div>
          )}

          {lines.length > 0 && (
            <pre
              ref={historyOutput}
              aria-label="Terminal history output"
              tabIndex={0}
              className="min-h-40 flex-1 overflow-auto rounded-[3px] border bg-background p-3 font-mono text-xs leading-5 whitespace-pre-wrap break-words outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30"
            >
              {lines.map((line, index) => (
                <span key={line.cursor} data-history-line="true">
                  {line.text}
                  {index < lines.length - 1 ? '\n' : ''}
                </span>
              ))}
            </pre>
          )}

          {!loading && !error && !queryValidationError && lines.length === 0 && (
            <p className="m-0 rounded-[3px] border border-dashed p-6 text-center text-[13px] text-muted-foreground">
              {hasMore
                ? query
                  ? 'No matches in this partial scan. Load older history to continue searching.'
                  : 'This partial page is empty. Older terminal history is available.'
                : query
                  ? 'No matching terminal history.'
                  : 'No terminal history recorded.'}
            </p>
          )}

          {!error && hasMore && (
            <Button
              ref={loadOlderButton}
              type="button"
              size="sm"
              variant="outline"
              aria-busy={loading}
              aria-disabled={loading}
              onClick={loadOlder}
            >
              {loading ? 'Loading older history…' : 'Load older history'}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
