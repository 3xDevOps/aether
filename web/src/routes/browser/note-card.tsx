import { useEffect, useRef } from 'react'
import { Pencil, Trash2, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { formatKeys } from '@/lib/keybindings'
import { closeNote, editNote, elementLabel, removeNote, updateNote, type Note } from './feedback'

/** One note's card: its editor while `draft` is set, what it says otherwise. */
export function NoteCard({ runID, note, index, onClose, onCaptures }: {
  runID: string
  note: Note
  index: number
  /** Closes a card that is only being read. */
  onClose: () => void
  onCaptures: (opener: HTMLElement | null) => void
}) {
  const coarse = useMediaQuery(coarsePointer)
  const field = useRef<HTMLTextAreaElement>(null)
  const editing = note.draft !== undefined
  const draft = note.draft ?? ''
  const shot = note.kind === 'screenshot'
  const element = shot ? undefined : note.element
  const failure = shot ? undefined : note.failure
  const title = shot ? 'Screenshot of the page' : element ? elementLabel(element) : failure ? 'The element could not be read' : 'Reading the element'
  // A screenshot is sent with or without words; an element needs the page's answer and something to say.
  const ready = shot || (element !== undefined && draft.trim() !== '')

  useEffect(() => {
    if (editing) field.current?.focus()
  }, [editing, note.id])

  return (
    <article aria-label={`Note ${index}: ${title}`} className="flex flex-col gap-2 rounded-panel border border-seam bg-raised px-3 py-2 text-ui text-text shadow-overlay">
      <header className="flex min-h-6 min-w-0 items-center gap-2">
        <span className="grid size-5 shrink-0 place-items-center rounded-full bg-accent text-ui-xs font-medium text-on-accent tabular-nums">{index}</span>
        <span className="flex min-w-0 flex-1 items-center gap-1.5 truncate font-medium">
          {!shot && !element && !failure && <Spinner className="size-3.5" />}
          <span className="truncate">{title}</span>
        </span>
        {!editing && (
          <>
            <Button variant="ghost" size="icon-sm" label="Edit note" onClick={() => editNote(runID, note.id)}><Pencil /></Button>
            <Button variant="ghost" size="icon-sm" label="Delete note" onClick={() => removeNote(runID, note.id)}><Trash2 /></Button>
            <Button variant="ghost" size="icon-sm" label="Close" onClick={onClose}><X /></Button>
          </>
        )}
      </header>
      {element && <p className="truncate font-code text-ui-sm text-muted" title={element.source ?? element.selector}>{element.source ?? element.selector}</p>}
      {failure && <p role="alert" className="break-words text-ui-sm text-state-failed">{failure}</p>}
      {editing ? (
        <>
          <Textarea
            ref={field}
            aria-label={`Note ${index}`}
            rows={3}
            value={draft}
            placeholder={shot ? 'Tell the agent what to look at (optional)' : 'Tell the agent what is wrong here'}
            onChange={(event) => updateNote(runID, note.id, { draft: event.target.value })}
            onKeyDown={(event) => {
              if (event.nativeEvent.isComposing) return
              if (event.key === 'Enter' && (event.metaKey || event.ctrlKey) && ready) {
                event.preventDefault()
                closeNote(runID, note.id, true)
              } else if (event.key === 'Escape') {
                // Escape with unsaved words would throw them away; it only closes a card that has none.
                event.preventDefault()
                if (draft.trim() === note.body) closeNote(runID, note.id, shot)
              }
            }}
          />
          <div className="flex min-w-0 items-center gap-2">
            <span className="min-w-0 flex-1">
              {shot && (
                <Button variant="link" size="sm" onClick={(event) => onCaptures(event.currentTarget)}>Keep as evidence…</Button>
              )}
            </span>
            {shot && note.body === ''
              ? <Button variant="ghost" size="sm" onClick={() => removeNote(runID, note.id)}>Remove</Button>
              : <Button variant="ghost" size="sm" onClick={() => closeNote(runID, note.id, false)}>Cancel</Button>}
            <Button variant="secondary" size="sm" hint={coarse ? undefined : `${shot ? 'Done' : 'Comment'} (${formatKeys('$mod+Enter')})`} disabled={!ready} onClick={() => closeNote(runID, note.id, true)}>
              {shot ? 'Done' : 'Comment'}
            </Button>
          </div>
        </>
      ) : (
        note.body && <p className="break-words whitespace-pre-wrap">{note.body}</p>
      )}
    </article>
  )
}
