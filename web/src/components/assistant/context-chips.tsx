import { useId, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { attachmentIds, attachmentLabel, entryIds, kindWord, pageLabel } from '@/components/assistant/labels'
import { X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import type { ViewAttachment, ViewContext, ViewEntry } from '@/lib/assistant-types'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore, type RootState } from '@/store'
import { assistantShareView, draftConflict, nextViewContext, saveShareView, viewContextKey } from '@/store/view-context'

/** The first answer to "which page?", focused when Send meets an unanswered one. */
export const conflictChoiceID = 'assistant-context-choice'

interface Row {
  kind: string
  label: string
  ids: string
}

/** An attachment that names the page itself: one chip shows both, the payload still carries both. */
function samePage(route: ViewEntry, attachment: ViewAttachment): boolean {
  if (attachment.kind === 'run') return route.run_id !== undefined && attachment.run_id === route.run_id
  return attachment.kind === 'mission' && route.run_id === undefined && route.mission_id !== undefined && attachment.mission_id === route.mission_id
}

/** The chips come from the payload itself, so they cannot show what the next message does not send. */
function model(s: RootState) {
  const share = assistantShareView(s)
  const moved = draftConflict(s)
  const payload = nextViewContext(s) ?? null
  const sent = payload?.attachments ?? []
  const route = payload?.route
  const folded = route ? sent.findIndex((attachment) => samePage(route, attachment)) : -1
  return {
    share,
    saving: s.assistantShareChoice !== null,
    pageRemoved: s.assistantPageRemoved,
    page: route && folded < 0 ? { label: pageLabel(s, route), ids: entryIds(route) } : null,
    /** The attachment whose chip also stands for the page, so removing it removes both. */
    folded,
    moved: moved && s.assistantPin ? { current: pageLabel(s, moved), started: pageLabel(s, s.assistantPin.page.route) } : null,
    attachments: sent.map((attachment, index) => attachmentRow(s, attachment, index)),
    payload: payload && viewContextKey(payload),
    rows: payload ? rows(s, payload) : [],
  }
}

type Model = ReturnType<typeof model>

function rows(s: RootState, payload: ViewContext): Row[] {
  const list: Row[] = []
  if (payload.route) list.push({ kind: kindWord.page, label: pageLabel(s, payload.route), ids: entryIds(payload.route) })
  const file = payload.open_file
  if (file) list.push({ kind: 'Open file', label: file.path, ids: [file.workspace_id, file.run_id].filter(Boolean).join(' · ') })
  if (payload.recent?.length) {
    list.push({ kind: 'Recent pages', label: `${payload.recent.length}`, ids: payload.recent.map((entry) => entryIds(entry) || entry.name).join(', ') })
  }
  payload.attachments?.forEach((attachment, index) => list.push(attachmentRow(s, attachment, index)))
  return list
}

/** The payload's attachments are the draft's, in order, so the draft's own labels them. */
function attachmentRow(s: RootState, attachment: ViewAttachment, index: number): Row {
  const held = s.assistantAttachments[index]
  return { kind: kindWord[attachment.kind], label: held ? attachmentLabel(s, held) : attachment.kind, ids: attachmentIds(attachment) }
}

const chip = 'inline-flex h-6 max-w-full min-w-0 items-center gap-1 rounded-full border border-seam bg-canvas pl-2 text-ui-sm'

function Chip({ row, onRemove }: { row: Row; onRemove: () => void }) {
  return (
    <li className="min-w-0 max-w-full">
      <span className={chip} title={row.ids}>
        <span className="shrink-0 text-muted">{row.kind}</span>
        <span className="min-w-0 truncate">{row.label}</span>
        <Button
          variant="ghost"
          size="icon-sm"
          data-chip-remove
          className="relative size-5 rounded-full coarse:size-5 after:absolute after:-inset-2 coarse:after:-inset-3"
          label={`Remove ${row.kind.toLowerCase()} ${row.label} from the message`}
          onClick={onRemove}
          onKeyDown={(event) => {
            if (event.key !== 'Delete' && event.key !== 'Backspace') return
            event.preventDefault()
            onRemove()
          }}
        >
          <X className="size-3" />
        </Button>
      </span>
    </li>
  )
}

function Inspect({ view, onShare, error }: { view: Model; onShare: (on: boolean) => void; error?: string }) {
  const [raw, setRaw] = useState(false)
  const sentID = useId()
  const rawID = useId()
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={view.share ? 'Inspect what is shared' : 'Page not shared: inspect what is shared'} className="h-6 px-1.5 font-normal text-muted">
          {view.share ? 'Inspect' : 'Page not shared'}
        </Button>
      </PopoverTrigger>
      <PopoverContent aria-label="What is shared" className="flex flex-col gap-3 text-ui-sm">
        <label className="flex items-center gap-2">
          <Checkbox checked={view.share} onCheckedChange={(checked) => onShare(checked === true)} />
          Share the page I am on with the assistant
        </label>
        {error && <p role="alert" className="break-words text-state-failed">{view.saving ? `${error}; retrying` : error}</p>}
        <section aria-labelledby={sentID} className="flex flex-col gap-1.5">
          <h3 id={sentID} className="font-medium">Sent with the next message</h3>
          {view.rows.length ? (
            <ul className="grid gap-1">
              {view.rows.map((row, index) => (
                <li key={index} className="grid min-w-0 grid-cols-[6rem_minmax(0,1fr)] gap-x-2">
                  <span className="text-muted">{row.kind}</span>
                  <span className="min-w-0 break-words">
                    {row.label}
                    {row.ids && <span className="block font-code break-all text-muted">{row.ids}</span>}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-muted">Nothing is sent, and the server forgets what it had, on every device.</p>
          )}
          {!view.share && view.rows.length > 0 && <p className="text-muted">Attachments you added are sent even while the page is not shared.</p>}
          {view.payload && (
            <>
              <Button variant="ghost" size="sm" aria-expanded={raw} aria-controls={raw ? rawID : undefined} className="self-start px-1" onClick={() => setRaw(!raw)}>
                {raw ? 'Hide raw JSON' : 'Raw JSON'}
              </Button>
              {raw && (
                <pre id={rawID} aria-label="Raw JSON" className="max-h-48 overflow-auto font-code whitespace-pre-wrap break-all text-muted">
                  {JSON.stringify(nextViewContext(useStore.getState()), null, 2)}
                </pre>
              )}
            </>
          )}
        </section>
      </PopoverContent>
    </Popover>
  )
}

/** The chips above the composer: what the next message is about, each removable, and exactly what it sends. */
export function ContextChips({ textarea }: { textarea: React.RefObject<HTMLTextAreaElement | null> }) {
  const key = useStore((s) => JSON.stringify(model(s)))
  const view = useMemo(() => JSON.parse(key) as Model, [key])
  const [error, setError] = useState<string>()
  const list = useRef<HTMLUListElement>(null)
  const askedID = useId()

  const setShare = (on: boolean) => {
    setError(undefined)
    saveShareView(useStore, api, on, (err) => setError(message(err)))
  }
  const removed = (index: number) => {
    requestAnimationFrame(() => {
      const buttons = Array.from(list.current?.querySelectorAll<HTMLElement>('[data-chip-remove]') ?? [])
      const next = buttons[Math.min(index, buttons.length - 1)] ?? textarea.current
      next?.focus()
    })
  }
  const offset = view.page ? 1 : 0

  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {view.moved && (
        <Callout tone="needs-you" role="group" aria-labelledby={askedID} className="text-ui-sm">
          <p id={askedID}>You moved to {view.moved.current}. Ask about:</p>
          <div className="mt-1 flex flex-wrap gap-1.5">
            <Button id={conflictChoiceID} size="sm" variant="secondary" className="max-w-full" onClick={() => useStore.getState().chooseAssistantPage(true)}>
              <span className="truncate">{view.moved.current}</span>
            </Button>
            <Button size="sm" variant="secondary" className="max-w-full" onClick={() => useStore.getState().chooseAssistantPage(false)}>
              <span className="truncate">{view.moved.started} (where you started)</span>
            </Button>
          </div>
        </Callout>
      )}
      <ul ref={list} aria-label="Sent with this message" className="flex min-w-0 flex-wrap items-center gap-1.5">
        {view.page && (
          <Chip
            row={{ kind: kindWord.page, label: view.page.label, ids: view.page.ids }}
            onRemove={() => {
              useStore.getState().setAssistantPageRemoved(true)
              removed(0)
            }}
          />
        )}
        {view.attachments.map((row, index) => (
          <Chip
            key={`${index}:${row.ids}`}
            row={row}
            onRemove={() => {
              useStore.getState().removeAssistantAttachment(index)
              if (index === view.folded) useStore.getState().setAssistantPageRemoved(true)
              removed(index + offset)
            }}
          />
        ))}
        <li className="flex items-center gap-1">
          {view.share && view.pageRemoved && (
            <Button variant="ghost" size="sm" className="h-6 px-1.5 font-normal text-muted" onClick={() => useStore.getState().setAssistantPageRemoved(false)}>
              Add this page
            </Button>
          )}
          <Inspect view={view} onShare={setShare} error={error} />
        </li>
      </ul>
    </div>
  )
}
