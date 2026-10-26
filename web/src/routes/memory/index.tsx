import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import { useShallow } from 'zustand/react/shallow'
import { useConnectionProblem } from '@/components/shell/connection'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { useLayoutGlide } from '@/lib/flip'
import { message } from '@/lib/format'
import { prefersReducedMotion, useDelayed } from '@/lib/hooks'
import { formatKeys } from '@/lib/keybindings'
import { type Motion, play, playEntrance } from '@/lib/motion'
import { allowed } from '@/lib/permissions'
import type { MemoryAddResult, MemoryItem } from '@/lib/types'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { AddNoteButton, Composer } from '@/routes/memory/add-note'
import { BriefRegion, BriefToggle } from '@/routes/memory/brief'
import { FooterBand, hintKey, Line, RowHint, UndoBar } from '@/routes/memory/footer'
import { NoteList } from '@/routes/memory/list'
import { byteLength, capNotice, listed, noteMaxBytes, tabCounts, type Filter } from '@/routes/memory/model'
import { NoteDialog } from '@/routes/memory/note'
import { NoteRow } from '@/routes/memory/row'
import { ShowBar } from '@/routes/memory/toolbar'
import { useNotes } from '@/routes/memory/use-notes'
import { useRemoval } from '@/routes/memory/use-removal'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'

const searchDelay = 250
const noticeMs = 4000
/** Rows that arrive together from elsewhere enter one by one only when this few; more just appear. */
const maxEntrances = 5

const glides = {
  pin: { duration: '--duration-glide', easing: '--ease-glide' },
  remove: { duration: '--duration-130', easing: '--ease-linear' },
  undo: { duration: '--duration-250', easing: '--ease-pop' },
} satisfies Record<string, Motion>

type Draft = { replaces?: MemoryItem; reopen: boolean }

const nearest = (row: Element, fallback: HTMLElement | null) =>
  (row.nextElementSibling ?? row.previousElementSibling)?.querySelector<HTMLElement>('[data-card-open]') ?? fallback

export function MemoryRoute({ client = api }: RouteProps & { client?: Api }) {
  const hydrated = useStore((s) => s.hydrated)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaces = useStore(useShallow((s) => Object.values(s.workspaces)))
  const canWrite = allowed('push', useSelf())
  const problem = useConnectionProblem()
  const mobile = useIsMobile()
  const { agents } = useAgentList(client)
  const agentNames = useMemo(() => agentDisplayNames(agents), [agents])
  const [picked, setPicked] = useState<string | null>(null)
  // Notes belong to one workspace, so there is no "all" reading.
  const workspaceID = picked ?? (activeWorkspace || workspaces[0]?.id || '')
  const [typed, setTyped] = useState('')
  const [words, setWords] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const [searching, setSearching] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [writing, setWriting] = useState({ text: '', edited: false })
  const [briefOpen, setBriefOpen] = useState(false)
  const [lookID, setLookID] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const notes = useNotes(client, workspaceID, words, filter)
  const removal = useRemoval(client, workspaceID, notes.refresh)
  const { load, refresh } = notes

  const listRef = useRef<HTMLDivElement>(null)
  const addRef = useRef<HTMLButtonElement>(null)
  const searchRef = useRef<HTMLButtonElement>(null)
  const refocus = useRef<'add' | 'search' | null>(null)
  const glideKind = useRef<keyof typeof glides>('pin')
  const capture = useLayoutGlide(listRef, glides[glideKind.current])
  const glide = useCallback((kind: keyof typeof glides) => {
    glideKind.current = kind
    capture()
  }, [capture])

  useEffect(() => {
    const timer = setTimeout(() => setWords(typed.trim()), searchDelay)
    return () => clearTimeout(timer)
  }, [typed])

  useEffect(() => {
    if (!notice) return
    const timer = setTimeout(() => setNotice(''), noticeMs)
    return () => clearTimeout(timer)
  }, [notice])

  useEffect(() => {
    setDraft(null)
    setOpen(null)
    setLookID(null)
  }, [workspaceID])

  useEffect(() => {
    const target = refocus.current === 'add' ? addRef.current : refocus.current === 'search' ? searchRef.current : null
    if (!target || (refocus.current === 'add' && draft) || (refocus.current === 'search' && searching)) return
    refocus.current = null
    target.focus()
  }, [draft, searching])

  const items = useMemo(
    () => (load.kind === 'ready' ? listed(load.items).filter((item) => !removal.hidden.has(item.id)) : []),
    [load, removal.hidden],
  )
  const counts = useMemo(() => (notes.all ? tabCounts(notes.all, removal.hidden) : {}), [notes.all, removal.hidden])
  const stale = load.kind === 'ready' && (load.filter !== filter || load.words !== words)
  const skeleton = useDelayed(load.kind === 'loading' && !notes.waiting)

  // Entrances only for what the viewer did not just read: a new filter or
  // search fades the list in, and a few rows arriving in place enter one by one.
  const shown = useRef<{ key: string; ids: Set<string> } | null>(null)
  const reveal = useRef<{ id: string; flash: boolean } | null>(null)
  useLayoutEffect(() => {
    if (load.kind !== 'ready') return
    const key = `${workspaceID}\n${load.filter}\n${load.words}`
    const before = shown.current
    shown.current = { key, ids: new Set(items.map((item) => item.id)) }
    // A note added outside this view is not revealed later, when the viewer moves to one that shows it.
    if (before && before.key !== key) reveal.current = null
    const rowOf = (id: string) => listRef.current?.querySelector<HTMLElement>(`[data-glide-key="${id}"]`)
    const target = reveal.current && rowOf(reveal.current.id)
    if (target && reveal.current) {
      target.scrollIntoView({ block: 'nearest' })
      if (reveal.current.flash && !prefersReducedMotion()) {
        // Read here, not through readToken's cache: --selection changes with the theme.
        const selection = getComputedStyle(document.documentElement).getPropertyValue('--selection').trim()
        play(target.querySelector('article') ?? target, [{ backgroundColor: selection }, { backgroundColor: 'transparent' }], { duration: '--duration-1000', easing: '--ease-standard' })
      }
      reveal.current = null
    }
    if (!before || !listRef.current) return
    if (before.key !== key) {
      playEntrance(listRef.current, { motion: { duration: '--duration-220', easing: '--ease-enter', delay: '--duration-100' }, rise: 3 })
      return
    }
    const added = items.filter((item) => !before.ids.has(item.id))
    if (added.length > maxEntrances) return
    added.forEach((item, index) => {
      const row = rowOf(item.id)
      if (row) playEntrance(row, { motion: { duration: '--duration-360', easing: '--ease-enter' }, rise: 4, index })
    })
  }, [load, items, workspaceID])

  const { patch } = notes
  const startRemoval = removal.remove
  // Stable handlers, so a hover or a keystroke does not re-render every row.
  const togglePin = useCallback(async (item: MemoryItem) => {
    try {
      const { item: next } = item.pinned_at ? await client.memoryUnpin(workspaceID, item.id) : await client.memoryPin(workspaceID, item.id)
      glide('pin')
      patch(next)
      refresh()
    } catch (error) {
      toast.error(message(error))
    }
  }, [client, workspaceID, glide, patch, refresh])

  const hide = useCallback((item: MemoryItem) => {
    glide('remove')
    startRemoval(item)
  }, [glide, startRemoval])

  // A removed row unmounts with its focus, so a keyboard user lands on the next row, else the previous one, else Add note.
  const listItem = useCallback((id: string) => listRef.current?.querySelector(`[data-glide-key="${id}"]`)?.closest('li'), [])

  const remove = useCallback((item: MemoryItem) => {
    const row = listItem(item.id)
    const next = row?.contains(document.activeElement) ? nearest(row, addRef.current) : null
    hide(item)
    next?.focus()
  }, [hide, listItem])

  const draftHeld = useRef(false)
  draftHeld.current = Boolean(draft) && writing.edited
  // Swapping the open composer would throw away what the viewer typed.
  const keepDraft = useCallback(() => {
    if (draftHeld.current) toast('Finish or cancel the note you are writing first.')
    return draftHeld.current
  }, [])

  const openNote = useCallback((item: MemoryItem) => setOpen(item.id), [])
  const lookAt = useCallback((item: MemoryItem | null) => setLookID(item?.id ?? null), [])
  const replace = useCallback((item: MemoryItem) => {
    if (!keepDraft()) setDraft({ replaces: item, reopen: false })
  }, [keepDraft])

  // Where focus goes once the detail closes: undefined restores the row that opened it, null leaves it in place.
  const afterDialog = useRef<HTMLElement | null | undefined>(undefined)

  const undo = () => {
    glide('undo')
    removal.undo()
  }

  const closeDraft = useCallback(() => {
    refocus.current = 'add'
    setDraft(null)
  }, [])

  const added = (result: MemoryAddResult) => {
    const old = draft?.replaces?.id
    const id = result.item.id
    removal.keep(id)
    setNotice(old
      ? `Replaced ${old} with ${id}${result.duplicate ? ', which already held this text' : ''}`
      : result.duplicate ? `Already kept as ${id}` : `Added ${id}`)
    reveal.current = { id, flash: Boolean(result.duplicate) }
    if (draft?.reopen) {
      setDraft(null)
      setOpen(id)
    } else {
      closeDraft()
    }
    refresh()
  }

  const openSearch = () => setSearching(true)
  const closeSearch = () => {
    refocus.current = 'search'
    setSearching(false)
    setTyped('')
    setWords('')
  }

  const look = lookID ? items.find((item) => item.id === lookID) : undefined
  const footer = (() => {
    if (removal.pending) return { key: `undo\n${removal.pending.id}`, node: <UndoBar item={removal.pending} onUndo={undo} /> }
    if (draft) {
      return {
        key: 'draft',
        node: <Line>{formatKeys('$mod+Enter')} adds · Esc cancels · <span aria-hidden>{byteLength(writing.text)} of {noteMaxBytes} bytes</span></Line>,
      }
    }
    if (notice) return { key: `notice\n${notice}`, node: <Line>{notice}</Line> }
    if (look) return { key: `look\n${hintKey(look)}`, node: <RowHint item={look} /> }
    if (load.kind !== 'ready' || items.length === 0) return { key: '', node: null }
    const text = capNotice(load, load.words)
      || (load.words
        ? `${items.length} ${items.length === 1 ? 'match' : 'matches'} for “${load.words}”`
        : `${items.length} ${items.length === 1 ? 'note' : 'notes'} · ${items.filter((item) => item.pinned_at).length} pinned`)
    return { key: text, node: <Line>{text}</Line> }
  })()

  const controls = workspaceID && (
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
      {load.kind !== 'unsupported' && <BriefToggle brief={notes.brief} open={briefOpen} onToggle={() => setBriefOpen((o) => !o)} />}
    </>
  )

  const ready = Boolean(workspaceID) && load.kind !== 'unsupported' && !notes.waiting

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Memory" actions={(!mobile && controls) || undefined} />
      <div className="flex min-h-0 flex-1 flex-col pb-[var(--keyboard-inset,0px)] sm:px-6 sm:py-4">
        <div className="mx-auto flex min-h-0 w-full max-w-3xl flex-1 flex-col overflow-hidden bg-canvas sm:rounded-panel sm:border sm:border-seam">
          {mobile && controls && (
            <div role="toolbar" aria-label="Memory actions" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-seam px-4 py-2">{controls}</div>
          )}
          {notes.waiting && (
            <Callout tone="neutral" role="status" className="m-4">{problem ?? 'Connecting'}. Team memory loads once the server connection is live.</Callout>
          )}
          {hydrated && !workspaceID && <EmptyState title="No workspace yet.">Team memory belongs to a workspace. Add one to start keeping notes.</EmptyState>}
          {load.kind === 'unsupported' && (
            <Callout tone="neutral" role="status" className="m-4">This server does not keep team memory. Update the server to use it.</Callout>
          )}
          {ready && (
            <>
              <BriefRegion brief={notes.brief} open={briefOpen} onRetry={() => void notes.readBrief()} />
              <ShowBar
                filter={filter}
                onFilter={setFilter}
                counts={counts}
                searching={searching}
                typed={typed}
                onTyped={setTyped}
                onOpenSearch={openSearch}
                onCloseSearch={closeSearch}
                searchRef={searchRef}
              />
              <NoteList
                load={load}
                items={items}
                stale={stale}
                skeleton={skeleton}
                listRef={listRef}
                onRetry={() => void notes.readList()}
                row={(item) => (
                  <NoteRow
                    item={item}
                    agents={agentNames}
                    canWrite={canWrite}
                    onOpen={openNote}
                    onLook={lookAt}
                    onPin={togglePin}
                    onReplace={replace}
                    onRemove={remove}
                  />
                )}
              />
              {canWrite && (draft
                ? (
                  <Composer
                    key={draft.replaces?.id ?? 'new'}
                    workspaceID={workspaceID}
                    client={client}
                    replaces={draft.replaces}
                    onAdded={added}
                    onRefresh={refresh}
                    onCancel={closeDraft}
                    onChange={setWriting}
                  />
                )
                : <AddNoteButton ref={addRef} onOpen={() => setDraft({ reopen: false })} />)}
              <FooterBand swapKey={footer.key}>{footer.node}</FooterBand>
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
          onChanged={refresh}
          onReplace={(item) => {
            if (keepDraft()) return
            afterDialog.current = null
            setOpen(null)
            setDraft({ replaces: item, reopen: true })
          }}
          onRemove={(item) => {
            const row = listItem(item.id)
            if (row) afterDialog.current = nearest(row, addRef.current)
            setOpen(null)
            hide(item)
          }}
          onClose={() => setOpen(null)}
          onCloseAutoFocus={(event) => {
            if (afterDialog.current === undefined) return
            event.preventDefault()
            afterDialog.current?.focus()
            afterDialog.current = undefined
          }}
        />
      )}
    </div>
  )
}

registerRoute('memory', MemoryRoute)
