import { useEffect, useRef } from 'react'
import { Pencil, Trash2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { DiffBlock } from '@/components/ui/session-blocks'
import { Textarea } from '@/components/ui/textarea'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { formatKeys } from '@/lib/keybindings'
import type { PatchLine } from '@/routes/diff/parse'
import { claimFocus, closeEditor, editComment, lineLabel, quotePatch, removeComment, updateComment, type ReviewComment } from '@/routes/diff/review'

/** One review comment: its editor while `draft` is set, the pinned text otherwise. */
export function CommentCard({
  runID,
  comment,
  lines,
  outdated = false,
  onRemoved,
}: {
  runID: string
  comment: ReviewComment
  /** The commented lines as the diff shows them now. */
  lines: PatchLine[]
  outdated?: boolean
  onRemoved?: () => void
}) {
  const coarse = useMediaQuery(coarsePointer)
  const field = useRef<HTMLTextAreaElement>(null)
  const edit = useRef<HTMLButtonElement>(null)
  const editing = comment.draft !== undefined
  const draft = comment.draft ?? ''
  const label = lineLabel(lines)
  const where = `${comment.path}:${label}`

  useEffect(() => {
    if (editing && claimFocus(comment.id)) field.current?.focus()
  }, [editing, comment.id, comment.lines])

  const close = (keep: boolean) => {
    const stays = Boolean(keep ? draft.trim() : comment.body)
    closeEditor(runID, comment.id, keep)
    if (stays) requestAnimationFrame(() => edit.current?.focus())
    else onRemoved?.()
  }

  return (
    <article aria-label={`Comment on ${where}`} className="flex max-w-[46rem] flex-col gap-2 rounded-panel border border-seam bg-canvas px-3 py-2 text-ui text-text">
      <header className="flex min-h-6 min-w-0 items-center gap-2 text-ui-sm text-muted">
        <span className="min-w-0 flex-1">
          {outdated ? 'Outdated: the diff no longer shows these lines. It is sent with this quote.' : `${label.includes('-') ? 'Lines' : 'Line'} ${label}`}
        </span>
        {!editing && (
          <>
            <Button ref={edit} variant="ghost" size="icon-sm" label="Edit comment" onClick={() => editComment(runID, comment)}>
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              label="Delete comment"
              onClick={() => {
                removeComment(runID, comment.id)
                onRemoved?.()
              }}
            >
              <Trash2 />
            </Button>
          </>
        )}
      </header>
      {outdated && <DiffBlock path={comment.path} patch={quotePatch(lines)} />}
      {editing ? (
        <>
          <Textarea
            ref={field}
            aria-label={`Comment on ${where}`}
            rows={3}
            value={draft}
            placeholder="Tell the agent what to change"
            onChange={(event) => updateComment(runID, comment.id, { draft: event.target.value })}
            onKeyDown={(event) => {
              if (event.nativeEvent.isComposing) return
              if (event.key === 'Enter' && (event.metaKey || event.ctrlKey) && draft.trim()) {
                event.preventDefault()
                close(true)
              } else if (event.key === 'Escape' && draft.trim() === comment.body) {
                event.preventDefault()
                close(false)
              }
            }}
          />
          <div className="flex min-w-0 items-center gap-2">
            <p className="min-w-0 flex-1 text-ui-sm text-muted">
              {!comment.body && !draft && !outdated && `${coarse ? 'Tap' : 'Shift-click'} another line to make it a range.`}
            </p>
            <Button variant="ghost" size="sm" onClick={() => close(false)}>
              Cancel
            </Button>
            <Button
              variant="secondary"
              size="sm"
              hint={coarse ? undefined : `Comment (${formatKeys('$mod+Enter')})`}
              disabled={!draft.trim()}
              onClick={() => close(true)}
            >
              Comment
            </Button>
          </div>
        </>
      ) : (
        <p className="break-words whitespace-pre-wrap">{comment.body}</p>
      )}
    </article>
  )
}
