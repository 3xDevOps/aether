import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Confirm } from '@/components/confirm'
import { Pin } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FailureCallout } from '@/components/ui/failure-callout'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { MemoryItem, MemoryShowResult } from '@/lib/types'
import { kindLabel, sourceLabel, superseded } from '@/routes/memory/model'
import { useStore } from '@/store'

function useName(id: string | undefined, name: string | undefined, fallback: string): string {
  const self = useStore((s) => s.info?.member.id)
  return id && id === self ? 'you' : name || fallback
}

export function PinBadge({ item }: { item: MemoryItem }) {
  const who = useName(item.pinned_by, item.pinned_by_name, 'a member')
  return (
    <Badge tone="review">
      <Pin aria-hidden />
      {item.scope === 'member' ? `Pinned for ${who}` : `Pinned by ${who}`}
    </Badge>
  )
}

export function Byline({ item, agents }: { item: MemoryItem; agents: Record<string, string> }) {
  const who = useName(item.author_id, item.author_name, 'an unknown member')
  const name = who.charAt(0).toUpperCase() + who.slice(1)
  return <>{item.harness ? `${name} · ${agents[item.harness] ?? item.harness}` : name}</>
}

export function RunLink({ id }: { id: string }) {
  const navigate = useStore((s) => s.navigate)
  const title = useStore((s) => s.runs[id]?.title)
  return (
    <Button variant="link" className="text-ui-sm" onClick={() => navigate('run', { runId: id })}>
      {title || 'Open run'}
    </Button>
  )
}

export function PRLink({ item }: { item: MemoryItem }) {
  return (
    <a href={item.pr_url} target="_blank" rel="noreferrer" className="text-accent hover:underline">
      {item.pr_number ? `PR #${item.pr_number}` : 'Pull request'}{item.pr_state && ` (${item.pr_state})`}
    </a>
  )
}

function Provenance({ item, agents, onOpen }: { item: MemoryItem; agents: Record<string, string>; onOpen: (id: string) => void }) {
  const rows: [string, ReactNode][] = [['Author', <Byline key="author" item={item} agents={agents} />]]
  if (item.run_id) rows.push(['Run', <span key="run" className="inline-flex flex-wrap items-center gap-2"><RunLink id={item.run_id} /><Code>{item.run_id}</Code></span>])
  if (item.commit) rows.push(['Commit', <Code key="commit">{item.commit.slice(0, 12)}</Code>])
  if (item.pr_url) rows.push(['Pull request', <PRLink key="pr" item={item} />])
  if (item.files?.length) {
    rows.push(['Files', <ul key="files" className="flex flex-col gap-0.5">{item.files.map((file) => <li key={file}><Code>{file}</Code></li>)}</ul>])
  }
  if (item.source_ref) rows.push([item.source === 'answer' ? 'Question' : 'Report', <Code key="ref">{item.source_ref}</Code>])
  rows.push(['Written', <RelativeTime key="written" at={item.created_at} />])
  if (item.pinned_at) rows.push(['Pinned', <RelativeTime key="pinned" at={item.pinned_at} />])
  if (item.superseded_by) {
    rows.push(['Superseded by', <Button key="by" variant="link" className="text-ui-sm" onClick={() => onOpen(item.superseded_by!)}>{item.superseded_by}</Button>])
  }
  return (
    <dl aria-label="Provenance" className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm [&_dt]:text-muted">
      {rows.map(([term, value]) => (
        <div key={term} className="contents">
          <dt>{term}</dt>
          <dd className="min-w-0 break-words">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function Chain({ notes, current, onOpen }: { notes: MemoryItem[]; current: string; onOpen: (id: string) => void }) {
  return (
    <section aria-label="Supersede chain" className="flex min-w-0 flex-col gap-1">
      <SectionLabel as="h3">Supersede chain, oldest first</SectionLabel>
      <ol className="flex min-w-0 flex-col gap-1 text-ui-sm">
        {notes.map((note) => (
          <li key={note.id} className="flex min-w-0 items-baseline gap-2">
            <RelativeTime at={note.created_at} compact className="w-8 shrink-0 tabular-nums text-muted" />
            {note.id === current
              ? <span aria-current="true" className="min-w-0 truncate font-medium">{note.title} (this note)</span>
              : <Button variant="link" className="min-w-0 truncate text-ui-sm" onClick={() => onOpen(note.id)}>{note.title}</Button>}
          </li>
        ))}
      </ol>
    </section>
  )
}

type Load = { kind: 'loading' } | { kind: 'ready'; result: MemoryShowResult } | { kind: 'failed'; message: string }

/** memory.show for one note; pins, unpins and removes it for a member who may write. */
export function NoteDialog({
  workspaceID,
  id,
  client,
  canWrite,
  agents,
  onOpen,
  onChanged,
  onClose,
}: {
  workspaceID: string
  id: string
  client: Api
  canWrite: boolean
  agents: Record<string, string>
  onOpen: (id: string) => void
  onChanged: () => void
  onClose: () => void
}) {
  const [load, setLoad] = useState<Load>({ kind: 'loading' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [removing, setRemoving] = useState(false)
  const request = useRef(0)

  const read = useCallback(async () => {
    const at = ++request.current
    setLoad({ kind: 'loading' })
    setError('')
    try {
      const result = await client.memoryShow(workspaceID, id)
      if (at === request.current) setLoad({ kind: 'ready', result })
    } catch (err) {
      if (at === request.current) setLoad({ kind: 'failed', message: message(err) })
    }
  }, [client, workspaceID, id])

  useEffect(() => {
    void read()
  }, [read])

  const act = async (call: () => Promise<{ item: MemoryItem }>) => {
    setBusy(true)
    setError('')
    try {
      const { item } = await call()
      setLoad((current) => (current.kind === 'ready' && current.result.item.id === item.id ? { kind: 'ready', result: { ...current.result, item } } : current))
      onChanged()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const item = load.kind === 'ready' ? load.result.item : undefined
  // The server orders the chain oldest first; this note follows the last note it replaced.
  const others = load.kind === 'ready' ? load.result.chain ?? [] : []
  const at = others.reduce((last, note, i) => (note.superseded_by === item?.id ? i + 1 : last), 0)
  const chain = item ? [...others.slice(0, at), item, ...others.slice(at)] : []

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="break-words">{item?.title ?? 'Note'}</DialogTitle>
          <DialogDescription>{item ? `${kindLabel[item.kind]} · ${sourceLabel[item.source]} · ${item.id}` : id}</DialogDescription>
        </DialogHeader>
        {load.kind === 'loading' && <p className="text-ui text-muted">Reading the note…</p>}
        {load.kind === 'failed' && <FailureCallout error={load.message} onRetry={() => void read()} />}
        {item && (
          <div className="flex min-w-0 flex-col gap-4">
            {(item.pinned_at || superseded(item)) && (
              <div className="flex flex-wrap gap-2">
                {item.pinned_at && <PinBadge item={item} />}
                {superseded(item) && <Badge tone="closed">Superseded</Badge>}
              </div>
            )}
            <p className="text-ui break-words whitespace-pre-wrap">{item.body}</p>
            <Provenance item={item} agents={agents} onOpen={onOpen} />
            {chain.length > 1 && <Chain notes={chain} current={item.id} onOpen={onOpen} />}
          </div>
        )}
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        {item && canWrite && (
          <DialogFooter>
            <Button variant="secondary" className="sm:mr-auto" disabled={busy} onClick={() => setRemoving(true)}>Remove…</Button>
            {item.pinned_at ? (
              <Button variant="secondary" disabled={busy} onClick={() => void act(() => client.memoryUnpin(workspaceID, item.id))}>Unpin</Button>
            ) : !superseded(item) && (
              <>
                <Button variant="secondary" disabled={busy} onClick={() => void act(() => client.memoryPin(workspaceID, item.id, 'member'))}>Pin for me only</Button>
                {item.scope === 'workspace' && <Button variant="secondary" disabled={busy} onClick={() => void act(() => client.memoryPin(workspaceID, item.id))}>Pin</Button>}
              </>
            )}
          </DialogFooter>
        )}
        {removing && item && (
          <Confirm
            title={`Remove ${item.id}?`}
            description="This deletes the note for everyone in the workspace. Runs that already started keep the brief they got."
            action="Remove note"
            pending="Removing…"
            onConfirm={() => client.memoryRm(workspaceID, item.id)}
            onDone={() => {
              onChanged()
              onClose()
            }}
            onClose={() => setRemoving(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
