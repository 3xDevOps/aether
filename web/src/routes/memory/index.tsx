import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import { useShallow } from 'zustand/react/shallow'
import { useConnectionProblem } from '@/components/shell/connection'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Card, CardControls, CardTitle } from '@/components/ui/card'
import { Code, CodeBlock } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { EmptyState } from '@/components/ui/empty-state'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Input } from '@/components/ui/input'
import { RelativeTime } from '@/components/ui/relative-time'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api, ApiError, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { allowed } from '@/lib/permissions'
import type { MemoryAddResult, MemoryItem } from '@/lib/types'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { AddNote } from '@/routes/memory/add-note'
import { briefMaxBytes, filters, kindLabel, listed, listLimit, reads, sourceLabel, superseded, type Filter } from '@/routes/memory/model'
import { Byline, NoteDialog, PinBadge, PRLink, RunLink } from '@/routes/memory/note'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'

const METHOD_NOT_FOUND = -32601
const searchDelay = 250

type Load =
  | { kind: 'loading' }
  | { kind: 'ready'; items: MemoryItem[]; full: boolean; words: string; filter: Filter }
  | { kind: 'failed'; message: string }
  | { kind: 'unsupported' }

type Brief = { kind: 'loading' } | { kind: 'ready'; text: string } | { kind: 'failed'; message: string }

function NoteRow({ item, agents, onOpen }: { item: MemoryItem; agents: Record<string, string>; onOpen: () => void }) {
  return (
    <Card>
      <div className="flex min-w-0 items-start justify-between gap-2">
        <CardTitle onOpen={onOpen}>{item.title}</CardTitle>
        <RelativeTime at={item.created_at} compact className="shrink-0 text-ui-sm tabular-nums text-muted" />
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-ui-sm text-muted">
        {item.pinned_at && <PinBadge item={item} />}
        {superseded(item) && <Badge tone="closed">Superseded</Badge>}
        <Badge>{kindLabel[item.kind]}</Badge>
        <span>{sourceLabel[item.source]}</span>
        <span aria-hidden>·</span>
        <span><Byline item={item} agents={agents} /></span>
        {item.run_id && <CardControls><RunLink id={item.run_id} /></CardControls>}
        {item.pr_url && <CardControls><PRLink item={item} /></CardControls>}
        {item.files?.length ? <span className="min-w-0 truncate font-code" title={item.files.join('\n')}>{item.files.length === 1 ? item.files[0] : `${item.files[0]} +${item.files.length - 1} more`}</span> : null}
      </div>
    </Card>
  )
}

function BriefPanel({ brief, onRetry }: { brief: Brief; onRetry: () => void }) {
  const size = brief.kind === 'ready' ? new TextEncoder().encode(brief.text).length : 0
  return (
    <Collapsible>
      <CollapsibleTrigger>
        Brief{' '}
        {brief.kind === 'ready' && <span className="text-ui-sm tabular-nums text-muted">· {size} of {briefMaxBytes} bytes</span>}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <section aria-label="Brief" className="flex min-w-0 flex-col gap-2 px-1 pt-2">
          <p className="text-ui-sm text-muted">What a run you launch in this workspace gets at start, byte for byte.</p>
          {brief.kind === 'loading' && <p className="text-ui text-muted">Reading the brief…</p>}
          {brief.kind === 'failed' && <FailureCallout error={brief.message} onRetry={onRetry} />}
          {brief.kind === 'ready' && (brief.text
            ? <CodeBlock className="break-words whitespace-pre-wrap">{brief.text}</CodeBlock>
            : <p className="text-ui text-muted">Empty: nothing is pinned, no run finished in the last 48 hours and there are no notes.</p>)}
        </section>
      </CollapsibleContent>
    </Collapsible>
  )
}

export function MemoryRoute({ client = api }: RouteProps & { client?: Api }) {
  const connection = useStore((s) => s.connection)
  const hydrated = useStore((s) => s.hydrated)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaces = useStore(useShallow((s) => Object.values(s.workspaces)))
  const self = useSelf()
  const canWrite = allowed('push', self)
  const mobile = useIsMobile()
  const problem = useConnectionProblem()
  const { agents } = useAgentList(client)
  const agentNames = useMemo(() => agentDisplayNames(agents), [agents])
  const [picked, setPicked] = useState<string | null>(null)
  // Notes belong to one workspace, so there is no "all" reading.
  const workspaceID = picked ?? (activeWorkspace || workspaces[0]?.id || '')
  const [typed, setTyped] = useState('')
  const [words, setWords] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const [load, setLoad] = useState<Load>({ kind: 'loading' })
  const [brief, setBrief] = useState<Brief>({ kind: 'loading' })
  const [open, setOpen] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const listRequest = useRef(0)
  const briefRequest = useRef(0)

  useEffect(() => {
    const timer = setTimeout(() => setWords(typed.trim()), searchDelay)
    return () => clearTimeout(timer)
  }, [typed])

  const readList = useCallback(async () => {
    const id = ++listRequest.current
    try {
      const results = await Promise.all(reads(workspaceID, words, filter).map((params) => client.memorySearch(params)))
      const items = results.flatMap((result) => result.items)
      const full = filter !== 'pinned' && results[results.length - 1].items.length >= listLimit
      if (id === listRequest.current) setLoad({ kind: 'ready', items, full, words, filter })
    } catch (error) {
      if (id !== listRequest.current) return
      // A 404 with a code is the server's own answer, such as an unknown workspace.
      const unsupported = error instanceof ApiError && (error.code === METHOD_NOT_FOUND || (error.status === 404 && error.code === undefined))
      setLoad(unsupported ? { kind: 'unsupported' } : { kind: 'failed', message: message(error) })
    }
  }, [client, workspaceID, words, filter])

  const readBrief = useCallback(async () => {
    const id = ++briefRequest.current
    try {
      const result = await client.memoryBrief(workspaceID)
      if (id === briefRequest.current) setBrief({ kind: 'ready', text: result.text })
    } catch (error) {
      if (id === briefRequest.current) setBrief({ kind: 'failed', message: message(error) })
    }
  }, [client, workspaceID])

  // A new search or filter keeps the list on screen until its answer lands; another workspace's notes do not.
  useEffect(() => {
    setLoad({ kind: 'loading' })
    setBrief({ kind: 'loading' })
  }, [workspaceID])

  useEffect(() => {
    if (connection === 'live' && workspaceID) void readList()
  }, [connection, workspaceID, readList])

  useEffect(() => {
    if (connection === 'live' && workspaceID) void readBrief()
  }, [connection, workspaceID, readBrief])

  const changed = useCallback(() => {
    void readList()
    void readBrief()
  }, [readList, readBrief])

  useEffect(() => useStore.subscribe((s, previous) => {
    if (s.memoryChanges[workspaceID] !== previous.memoryChanges[workspaceID]) changed()
  }), [workspaceID, changed])

  const added = (result: MemoryAddResult) => {
    setAdding(false)
    toast.success(result.duplicate ? `Team memory already holds this note as ${result.item.id}` : `Added ${result.item.id}`)
    changed()
  }

  const waiting = load.kind === 'loading' && connection !== 'live'
  const skeleton = useDelayed(load.kind === 'loading' && !waiting)
  const items = load.kind === 'ready' ? listed(load.items) : []

  const controls = (
    <>
      {workspaces.length > 1 && (
        <Select value={workspaceID} onValueChange={setPicked}>
          <SelectTrigger aria-label="Workspace" className="w-40 max-w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {workspaces.map((w) => <SelectItem key={w.id} value={w.id}>{w.name}</SelectItem>)}
          </SelectContent>
        </Select>
      )}
      {canWrite && workspaceID && <Button variant="secondary" size="sm" onClick={() => setAdding(true)}>Add note</Button>}
    </>
  )

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Memory" actions={mobile ? undefined : controls} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-4xl min-w-0 flex-col gap-4 px-4 py-6 sm:px-6">
          {mobile && <div role="toolbar" aria-label="Memory actions" className="flex flex-wrap items-center gap-2">{controls}</div>}
          {waiting && (
            <Callout tone="neutral" role="status">{problem ?? 'Connecting'}. Team memory loads once the server connection is live.</Callout>
          )}
          {hydrated && !workspaceID && <EmptyState title="No workspace yet.">Team memory belongs to a workspace. Add one to start keeping notes.</EmptyState>}
          {load.kind === 'unsupported' && (
            <Callout tone="neutral" role="status">This server does not keep team memory. Update the server to use it.</Callout>
          )}
          {workspaceID && load.kind !== 'unsupported' && !waiting && (
            <>
              <BriefPanel brief={brief} onRetry={() => void readBrief()} />
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <Input
                  type="search"
                  aria-label="Search notes"
                  placeholder="Search titles, text and files"
                  className="min-w-0 flex-1 basis-56"
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                />
                <div role="group" aria-label="Show" className="flex flex-wrap items-center gap-0.5">
                  {filters.map((f) => (
                    <Button key={f.value} size="sm" variant={filter === f.value ? 'secondary' : 'ghost'} aria-pressed={filter === f.value} onClick={() => setFilter(f.value)}>
                      {f.label}
                    </Button>
                  ))}
                </div>
              </div>
              {load.kind === 'loading' && skeleton && (
                <div aria-hidden className="flex flex-col gap-2">
                  {[0, 1, 2].map((i) => <Skeleton key={i} className="h-16" />)}
                </div>
              )}
              {load.kind === 'failed' && <FailureCallout error={load.message} onRetry={() => void readList()} />}
              {load.kind === 'ready' && items.length === 0 && (
                load.words || load.filter !== 'all'
                  ? <p className="text-ui text-muted">{filters.find((f) => f.value === load.filter)!.empty}{load.words ? <> match “{load.words}”</> : null}.</p>
                  : (
                    <EmptyState title="No notes yet." action={canWrite && <Button variant="secondary" onClick={() => setAdding(true)}>Add note</Button>}>
                      Runs add decisions, conventions and gotchas with <Code>aether-internal memory add</Code>, and each run that reports success or failure records its outcome here.
                    </EmptyState>
                  )
              )}
              {items.length > 0 && (
                <ul aria-label="Notes" className="flex min-w-0 flex-col gap-2">
                  {items.map((item) => (
                    <li key={item.id}>
                      <NoteRow item={item} agents={agentNames} onOpen={() => setOpen(item.id)} />
                    </li>
                  ))}
                </ul>
              )}
              {load.kind === 'ready' && load.full && (
                <p className="text-ui-sm text-muted">{load.words ? `Every pinned match, then the ${listLimit} best matches.` : `Every pinned note, then the ${listLimit} newest notes. Search to find older ones.`}</p>
              )}
            </>
          )}
        </div>
      </div>
      {open && workspaceID && (
        <NoteDialog
          workspaceID={workspaceID}
          id={open}
          client={client}
          canWrite={canWrite}
          agents={agentNames}
          onOpen={setOpen}
          onChanged={changed}
          onClose={() => setOpen(null)}
        />
      )}
      {adding && workspaceID && (
        <AddNote workspaceID={workspaceID} client={client} onAdded={added} onFailed={changed} onClose={() => setAdding(false)} />
      )}
    </div>
  )
}

registerRoute('memory', MemoryRoute)
