import { type Ref, useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { Plus } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FadeStrip } from '@/components/ui/fade-strip'
import { Label } from '@/components/ui/label'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { playEntrance } from '@/lib/motion'
import type { MemoryAddParams, MemoryAddResult, MemoryItem } from '@/lib/types'
import { kindLabel, writableKinds } from '@/routes/memory/model'

export function AddNoteButton({ onOpen, ref }: { onOpen: () => void; ref: Ref<HTMLButtonElement> }) {
  return (
    <div className="shrink-0 border-t border-seam px-1.5 py-1.5">
      <Button ref={ref} variant="ghost" className="h-8 w-full justify-start gap-3 px-2.5 font-normal coarse:h-11" onClick={onOpen}>
        <Plus aria-hidden />
        Add note
      </Button>
    </div>
  )
}

/**
 * `memory.add` as a member, or with `replaces` the note that supersedes it.
 * A refusal is the server's text, unchanged, and keeps the draft; a refused
 * pin still stores the note, so every failure re-reads the list, and so does
 * an add that finishes after the composer closed.
 */
export function Composer({
  workspaceID,
  client,
  replaces,
  onAdded,
  onRefresh,
  onCancel,
  onChange,
}: {
  workspaceID: string
  client: Api
  replaces?: MemoryItem
  onAdded: (result: MemoryAddResult) => void
  onRefresh: () => void
  onCancel: () => void
  onChange: (state: { text: string; edited: boolean }) => void
}) {
  const [kind, setKind] = useState<MemoryAddParams['kind']>(replaces && replaces.kind !== 'outcome' ? replaces.kind : 'decision')
  const [text, setText] = useState(replaces?.body ?? '')
  const [files, setFiles] = useState(replaces?.files?.join('\n') ?? '')
  const [onlyMe, setOnlyMe] = useState(replaces?.scope === 'member')
  const [pin, setPin] = useState(false)
  const [start] = useState({ kind, text, files, onlyMe })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const open = useRef(true)
  const box = useRef<HTMLElement>(null)
  const id = useId()
  const [action, pending] = replaces ? ['Replace note', 'Replacing…'] : ['Add note', 'Adding…']
  // The server moves a replaced note's pin, in its own scope, to the replacement.
  const pinMoves = Boolean(replaces?.pinned_at)

  useEffect(() => {
    open.current = true
    return () => {
      open.current = false
    }
  }, [])

  useLayoutEffect(() => {
    if (box.current) playEntrance(box.current, { motion: { duration: '--duration-expand', easing: '--ease-enter' }, rise: 4 })
  }, [])

  const edited = kind !== start.kind || text.trim() !== start.text.trim() || files.trim() !== start.files.trim() || onlyMe !== start.onlyMe || pin
  useEffect(() => onChange({ text, edited }), [onChange, text, edited])

  const add = async () => {
    if (busy || !text.trim()) return
    setBusy(true)
    setError('')
    const cited = files.split('\n').map((file) => file.trim()).filter(Boolean)
    try {
      const result = await client.memoryAdd({
        workspace_id: workspaceID,
        kind,
        text,
        ...(cited.length ? { files: cited } : {}),
        ...(replaces ? { supersedes: replaces.id } : {}),
        ...(onlyMe ? { scope: 'member' as const } : {}),
        ...(pin ? { pin } : {}),
      })
      if (open.current) onAdded(result)
      else onRefresh()
    } catch (err) {
      if (open.current) setError(message(err))
      onRefresh()
    } finally {
      if (open.current) setBusy(false)
    }
  }

  return (
    <section
      ref={box}
      aria-label={replaces ? `Replace ${replaces.id}` : 'Add a note'}
      className="flex min-h-0 shrink flex-col gap-2.5 overflow-y-auto overscroll-contain border-t border-seam px-4 pt-3 pb-3"
      onKeyDown={(e) => {
        if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
          e.preventDefault()
          void add()
          return
        }
        if (e.key !== 'Escape' || e.defaultPrevented) return
        e.preventDefault()
        e.stopPropagation()
        onCancel()
      }}
    >
      {replaces && (
        <p className="min-w-0 truncate text-ui-sm text-muted">
          Replacing “{replaces.title}”. The old note stays in history as Superseded.
        </p>
      )}
      <Textarea
        autoFocus
        aria-label="Note"
        aria-describedby={`${id}-note`}
        rows={3}
        placeholder="The first line is the title"
        className="max-h-[40dvh] min-h-18"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <p id={`${id}-note`} className="sr-only">The first line is the title, at most 120 bytes; the note holds at most 1024 bytes.</p>
      <div className="flex min-w-0 items-center gap-2">
        <span id={`${id}-kind`} className="shrink-0 text-ui-sm text-muted">Kind</span>
        <FadeStrip>
          <RadioPills aria-labelledby={`${id}-kind`} value={kind} onValueChange={(value) => setKind(value as MemoryAddParams['kind'])}>
            {writableKinds.map((k) => <RadioPillsItem key={k} value={k} className="h-6 px-2 text-ui-sm">{kindLabel[k]}</RadioPillsItem>)}
          </RadioPills>
        </FadeStrip>
      </div>
      <Collapsible defaultOpen={Boolean(replaces?.files?.length)}>
        <CollapsibleTrigger className="-mx-1 w-auto text-ui-sm text-muted">Files</CollapsibleTrigger>
        <CollapsibleContent>
          <Textarea
            aria-label="Files"
            aria-describedby={`${id}-files`}
            rows={2}
            className="mt-1 font-code text-ui-sm"
            value={files}
            onChange={(e) => setFiles(e.target.value)}
          />
          <p id={`${id}-files`} className="mt-1 text-ui-sm text-muted">Repository paths the note cites, one per line: at most 20, each at most 512 bytes.</p>
        </CollapsibleContent>
      </Collapsible>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Checkbox id={`${id}-only-me`} checked={onlyMe} disabled={pinMoves} onCheckedChange={(checked) => setOnlyMe(checked === true)} />
          <Label htmlFor={`${id}-only-me`}>Pin to my runs only</Label>
        </div>
        {!pinMoves && (
          <div className="flex items-center gap-2">
            <Checkbox id={`${id}-pin`} checked={pin} onCheckedChange={(checked) => setPin(checked === true)} />
            <Label htmlFor={`${id}-pin`}>Pin</Label>
          </div>
        )}
        <div className="ml-auto flex items-center gap-1.5">
          <Button variant="ghost" onClick={onCancel}>Cancel</Button>
          <Button disabled={busy || !text.trim()} onClick={() => void add()}>{busy ? pending : action}</Button>
        </div>
      </div>
      <p className="text-ui-sm text-muted">{pinMoves
          ? 'The pin moves to the replacement, at the note\'s scope.'
          : pin
            ? onlyMe
              ? 'Your new runs get its title in the brief. Everyone in the workspace can still read it.'
              : 'Every new run in this workspace gets its title in the brief.'
            : onlyMe
              ? 'A pin on it enters only your own runs\' briefs. Everyone in the workspace can still read it.'
              : 'Every run in this workspace can search it.'}</p>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
    </section>
  )
}
