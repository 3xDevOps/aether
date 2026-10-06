import { useCallback, useEffect, useRef, useState } from 'react'
import { MissingRun } from '@/components/missing-run'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import type { DevBrowserCloseParams, DevBrowserNavigateParams, DevBrowserPage, DevBrowserResetParams, DevBrowserStatusResult, DevController, DevControlFence, DevSurface } from '@/lib/types'
import { cn, field } from '@/lib/utils'
import { useStore } from '@/store'
import { isTerminal } from '@/store/runs'
import { BrowserSurface } from './surface'

// A tab-local identity survives pane detach but is never cloned through
// sessionStorage into another window. Reloads observe first, then take control.
let tabControlSession: string | undefined

const stateWords: Record<string, string> = {
  not_started: 'Browser not started',
  creating: 'Browser starting',
  running: 'Browser running',
  paused: 'Browser paused',
  session_lost: 'Browser session lost',
  unavailable: 'Browser unavailable',
}

type BrowserConfirmation =
  | { action: 'close'; target: DevBrowserCloseParams }
  | { action: 'reset'; target: DevBrowserResetParams }

export function BrowserView({ runID }: { runID: string }) {
  const identity = useStore((state) => state.identityKey)
  return <BrowserRoute key={`${identity}:${runID}`} runID={runID} />
}

function BrowserRoute({ runID }: { runID: string }) {
  const run = useStore((state) => state.runs[runID])
  const members = useStore((state) => state.members)
  const [controlSession] = useState(() => tabControlSession ??= crypto.randomUUID())
  const [status, setStatus] = useState<DevBrowserStatusResult | null>(null)
  const [pages, setPages] = useState<DevBrowserPage[]>([])
  const [selected, setSelected] = useState('')
  const [controller, setController] = useState<DevController | null>(null)
  const [address, setAddress] = useState('http://localhost:3000')
  const [preset, setPreset] = useState('1280x800')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [capture, setCapture] = useState('')
  const [connection, setConnection] = useState(0)
  const [blocked, setBlocked] = useState(false)
  const [enlarged, setEnlarged] = useState(false)
  const [toolsOpen, setToolsOpen] = useState(false)
  const [confirmation, setConfirmation] = useState<BrowserConfirmation | null>(null)
  const toolsTrigger = useRef<HTMLButtonElement>(null)
  const openingDialog = useRef(false)
  const inFlight = useRef(false)
  const alive = useRef(true)
  const reading = useRef(false)
  const mutation = useRef(0)
  const focused = useRef(!document.hidden)
  const owned = useRef<{ surface: DevSurface; fence: DevControlFence } | null>(null)
  const selectedPage = pages.find((page) => page.page_id === selected)
  const expanded = enlarged && Boolean(selectedPage)
  const owns = controller?.control_session_id === controlSession
  const fence: DevControlFence | null = owns && controller ? { control_session_id: controlSession, control_generation: controller.control_generation } : null
  const surface: DevSurface | null = status?.session_id ? { kind: 'browser', id: 'browser', incarnation: status.session_id } : null
  if (surface) owned.current = fence ? { surface, fence } : null

  const release = useCallback(async (held = owned.current) => {
    if (!held) return
    owned.current = null
    mutation.current++
    if (alive.current) {
      setController(null)
      setBlocked(true)
    }
    try {
      await api.devControlRelease({
        run_id: runID, surface: held.surface,
        control_session_id: held.fence.control_session_id,
        control_generation: held.fence.control_generation,
      })
    }
    catch (cause) { if (alive.current) setError(message(cause)) }
  }, [runID])

  useEffect(() => {
    focused.current = !document.hidden
    const blur = () => { focused.current = false; void release() }
    const focus = () => { focused.current = !document.hidden }
    const visibility = () => { if (document.hidden) blur(); else focus() }
    window.addEventListener('blur', blur)
    window.addEventListener('focus', focus)
    document.addEventListener('visibilitychange', visibility)
    return () => {
      window.removeEventListener('blur', blur)
      window.removeEventListener('focus', focus)
      document.removeEventListener('visibilitychange', visibility)
      focused.current = false
      alive.current = false
      void release()
    }
  }, [release])

  const refresh = useCallback(async () => {
    if (reading.current) return
    reading.current = true
    const version = mutation.current
    try {
      const next = await api.devBrowserStatus({ run_id: runID })
      const [inventory, ownership] = await Promise.all([
        next.running && next.session_id ? api.devBrowserPages({ run_id: runID, session_id: next.session_id }) : null,
        next.session_id ? api.devControlStatus({ run_id: runID, surface: { kind: 'browser', id: 'browser', incarnation: next.session_id } }) : null,
      ])
      if (!alive.current || version !== mutation.current) return
      setStatus(next)
      setPages(inventory?.pages ?? [])
      setSelected(inventory?.selected_page_id ?? next.selected_page_id ?? '')
      if (ownership?.controller?.control_session_id === controlSession && !focused.current) {
        void release({ surface: ownership.surface, fence: ownership.controller })
      } else setController(ownership?.controller ?? null)
    } catch (cause) {
      if (alive.current) {
        setError(message(cause))
        setBlocked(true)
      }
    } finally { reading.current = false }
  }, [runID, controlSession, release])

  const ended = run ? isTerminal(run.status) : false
  useEffect(() => {
    alive.current = true
    void refresh()
    const timer = ended ? undefined : setInterval(() => { if (!document.hidden) void refresh() }, 1500)
    return () => { alive.current = false; clearInterval(timer) }
  }, [refresh, ended])
  useEffect(() => {
    if (selectedPage) setAddress(selectedPage.url)
  }, [selectedPage?.url, selectedPage?.page_id])
  useEffect(() => {
    if (selectedPage) setPreset(`${selectedPage.width}x${selectedPage.height}`)
  }, [selectedPage?.width, selectedPage?.height, selectedPage?.page_id])

  const perform = async (operation: () => Promise<void>) => {
    if (inFlight.current) return
    inFlight.current = true
    setBusy(true)
    setError('')
    mutation.current++
    try { await operation() }
    catch (cause) { if (alive.current) setError(message(cause)) }
    finally {
      mutation.current++
      inFlight.current = false
      if (alive.current) {
        setBusy(false)
        await refresh()
      }
    }
  }
  const updatePage = (page: DevBrowserPage) => {
    mutation.current++
    setPages((previous) => previous.map((item) => item.page_id === page.page_id && item.session_id === page.session_id && item.page_revision <= page.page_revision ? page : item))
  }
  const acquire = (takeover: boolean) => {
    if (!surface) return
    void perform(async () => {
      const result = await api.devControlAcquire({ run_id: runID, surface, control_session_id: controlSession, expected_generation: controller?.control_generation ?? 0, takeover })
      if (!alive.current || !focused.current) {
        if (result.controller) await release({ surface, fence: result.controller })
        return
      }
      owned.current = result.controller ? { surface, fence: result.controller } : null
      setController(result.controller)
      setBlocked(false)
    })
  }
  const open = () => {
    if (!status || (!fence && status.session_id)) return
    void perform(async () => {
      const [width, height] = preset.split('x').map(Number)
      const result = await api.devBrowserOpen({ run_id: runID, session_id: status.session_id, control_session_id: fence?.control_session_id ?? controlSession, control_generation: fence?.control_generation ?? 0, url: address, width, height })
      const held = { surface: { kind: 'browser' as const, id: 'browser', incarnation: result.page.session_id }, fence: result.control }
      if (!alive.current || !focused.current) { await release(held); return }
      owned.current = held
      setPages((previous) => [...previous.filter((page) => page.page_id !== result.page.page_id), result.page])
      setSelected(result.page.page_id)
      setBlocked(false)
    })
  }
  const navigatePage = (direction: DevBrowserNavigateParams['direction']) => {
    if (!selectedPage || !fence) return
    void perform(async () => {
      const result = await api.devBrowserNavigate({ run_id: runID, session_id: selectedPage.session_id, page_id: selectedPage.page_id, page_revision: selectedPage.page_revision, ...fence, direction, url: direction === 'url' ? address : undefined, timeout_ms: 15000 })
      updatePage(result.page)
    })
  }

  const ask = (action: BrowserConfirmation['action']) => {
    if (inFlight.current || blocked || !status?.session_id || !fence) return
    if (action === 'close') {
      if (!selectedPage || !status.running) return
      setConfirmation({ action, target: { run_id: runID, session_id: selectedPage.session_id, page_id: selectedPage.page_id, page_revision: selectedPage.page_revision, ...fence } })
    } else setConfirmation({ action, target: { run_id: runID, session_id: status.session_id, ...fence } })
    setError('')
    openingDialog.current = true
    setToolsOpen(false)
  }
  const confirmationCurrent = Boolean(confirmation && !blocked && fence
    && status?.session_id === confirmation.target.session_id
    && fence.control_session_id === confirmation.target.control_session_id
    && fence.control_generation === confirmation.target.control_generation
    && (confirmation.action === 'reset' || (status.running
      && selectedPage?.session_id === confirmation.target.session_id
      && selectedPage.page_id === confirmation.target.page_id
      && selectedPage.page_revision === confirmation.target.page_revision)))
  const confirm = () => {
    if (!confirmation || !confirmationCurrent || !focused.current || inFlight.current) return
    const captured = confirmation
    void perform(async () => {
      if (captured.action === 'close') await api.devBrowserClose(captured.target)
      else {
        await api.devBrowserReset(captured.target)
        setPages([])
        setController(null)
        setBlocked(false)
      }
      setConfirmation(null)
    })
  }

  if (!run) return <MissingRun />
  const writable = Boolean(fence && !busy && !blocked && status?.running)
  const driver = owns ? 'You are driving' : controller ? `${controller.kind === 'run_agent' ? 'The agent' : members[controller.member_id ?? '']?.display_name ?? 'Another member'} is driving` : 'Nobody is driving'
  const state = `${status ? stateWords[status.state] ?? 'Browser unavailable' : error ? 'Browser unavailable' : 'Checking the browser'} · ${driver}`
  const canOpen = Boolean(status?.available && (!status.session_id || fence))
  const take = surface && (!owns || blocked) && (
    <Button variant="secondary" disabled={busy} onClick={() => acquire(Boolean(controller && !owns))}>{controller && !owns ? 'Take over' : 'Take control'}</Button>
  )

  return <div className="flex h-full min-h-0 min-w-0 flex-col">
    <section className="flex min-h-0 min-w-0 flex-1 flex-col gap-3 overflow-y-auto p-3">
      {selectedPage && <div className={`${expanded ? 'hidden' : 'flex'} shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-seam pb-3 text-ui`}>
        <p role="status" className="min-w-0 flex-1 basis-60 break-words text-ui-sm text-muted">{state}</p>
        <div className="flex flex-wrap items-center gap-2">
          {take}
          {fence && <Button variant="secondary" disabled={busy} onClick={() => void perform(() => release())}>Release control</Button>}
        </div>
      </div>}
      {status?.reason && <p role="alert" className="break-words text-ui text-state-failed">{status.reason}</p>}
      {error && !confirmation && <p role="alert" className="break-words text-ui text-state-failed">{error}</p>}
      {selectedPage ? <>
      <form className={`${expanded ? 'hidden' : 'flex'} min-w-0 shrink-0 flex-wrap items-center gap-2`} onSubmit={(event) => { event.preventDefault(); navigatePage('url') }}>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="secondary" aria-label="Back" disabled={!writable} onClick={() => navigatePage('back')}>Back</Button>
          <Button type="button" variant="secondary" aria-label="Forward" disabled={!writable} onClick={() => navigatePage('forward')}>Forward</Button>
          <Button type="button" variant="secondary" disabled={!writable} onClick={() => navigatePage('reload')}>Reload page</Button>
        </div>
        <div className="flex min-w-0 flex-1 basis-80 items-center gap-2">
          <Input aria-label="Browser URL" type="url" value={address} onChange={(event) => setAddress(event.target.value)} className="min-w-0 flex-1" />
          <Button type="submit" disabled={!writable}>Go</Button>
        </div>
      </form>
      <div className={`${expanded ? 'hidden' : 'flex'} min-w-0 shrink-0 flex-wrap items-center gap-x-3 gap-y-2`}>
        <Popover open={toolsOpen} onOpenChange={setToolsOpen}>
          <PopoverTrigger asChild><Button ref={toolsTrigger} variant="secondary">Page tools</Button></PopoverTrigger>
          <PopoverContent aria-label="Page tools" className="space-y-3" onCloseAutoFocus={(event) => {
            if (!openingDialog.current) return
            openingDialog.current = false
            event.preventDefault()
          }}>
        <label className="flex min-w-0 items-center gap-1 text-ui">
          <span className="w-14 shrink-0">Page</span>
          <select aria-label="Browser page" value={selected} disabled={!writable} className={cn(field, 'min-w-0 flex-1 coarse:h-11 coarse:min-h-11')} onChange={(event) => {
            const page = pages.find((item) => item.page_id === event.target.value)
            if (!page || !fence) return
            void perform(async () => { await api.devBrowserAction({ run_id: runID, session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision, ...fence, action: 'select' }); setSelected(page.page_id) })
          }}>
            {pages.map((page) => <option key={page.page_id} value={page.page_id}>{page.title || page.url || 'Untitled page'}</option>)}
          </select>
        </label>
        <label className="flex min-w-0 items-center gap-1 text-ui">
          <span className="w-14 shrink-0">Viewport</span>
          <select aria-label="Browser viewport" value={preset} className={cn(field, 'min-w-0 flex-1 coarse:h-11 coarse:min-h-11')} disabled={busy || !fence || blocked} onChange={(event) => {
            if (!fence || blocked) return
            const [width, height] = event.target.value.split('x').map(Number)
            void perform(async () => { const result = await api.devBrowserViewport({ run_id: runID, session_id: selectedPage.session_id, page_id: selectedPage.page_id, page_revision: selectedPage.page_revision, ...fence, width, height }); updatePage(result.page) })
          }}>
            <option value="1280x800">Desktop · 1280 × 800</option>
            <option value="390x844">Phone · 390 × 844</option>
            <option value="844x390">Phone landscape · 844 × 390</option>
            {!['1280x800', '390x844', '844x390'].includes(preset) && <option value={preset}>Current · {preset.replace('x', ' × ')}</option>}
          </select>
        </label>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="secondary" disabled={busy || blocked || !status?.available || !fence} onClick={open}>New page</Button>
          <Button variant="secondary" disabled={busy} onClick={() => {
            void perform(async () => { const result = await api.devBrowserScreenshot({ run_id: runID, session_id: selectedPage.session_id, page_id: selectedPage.page_id, page_revision: selectedPage.page_revision }); setCapture(result.artifact.id) })
          }}>Screenshot</Button>
          <Button variant="ghost" disabled={busy} onClick={() => void perform(async () => { await refresh(); setConnection((value) => value + 1); setBlocked(false) })}>Reconnect</Button>
        </div>
        <div className="flex flex-wrap items-center gap-2 border-t border-seam pt-3">
          <Button variant="secondary" disabled={!writable} onClick={() => ask('close')}>Close page</Button>
          <Button variant="secondary" disabled={!fence || busy || blocked} onClick={() => ask('reset')}>Reset session</Button>
        </div>
          </PopoverContent>
        </Popover>
      </div>
      {capture && <p role="status" hidden={expanded} className="break-words text-ui-sm text-muted">Screenshot saved. Open Captures from More to keep it; nothing has been published.</p>}
      <BrowserSurface key={`${selectedPage.session_id}:${selectedPage.page_id}`} runID={runID} page={selectedPage} control={!busy && !blocked ? fence : null} connection={connection}
        expanded={expanded} onExpandedChange={setEnlarged} onPage={updatePage} onError={(text) => { setError(text); setBlocked(true); void release() }} />
      </> : (
        <EmptyState
          title="No page open"
          action={canOpen
            ? <Button disabled={busy} onClick={open}>Open {address}</Button>
            : take || (fence && status?.session_id && <Button variant="secondary" disabled={busy || blocked} onClick={() => ask('reset')}>Reset session</Button>)}
        >
          <span role="status">{state}</span>
        </EmptyState>
      )}
    </section>
    <AlertDialog open={confirmation !== null} onOpenChange={(open) => { if (!open && !inFlight.current) setConfirmation(null) }}>
      <AlertDialogContent onCloseAutoFocus={(event) => { event.preventDefault(); toolsTrigger.current?.focus() }}>
        <AlertDialogHeader>
          <AlertDialogTitle>{confirmation?.action === 'close' ? 'Close shared page?' : 'Reset shared browser session?'}</AlertDialogTitle>
          <AlertDialogDescription>{confirmation?.action === 'close'
            ? 'Close this shared page for everyone? Other pages and the browser session remain.'
            : 'All pages, cookies and logins will be lost.'}</AlertDialogDescription>
        </AlertDialogHeader>
        {!confirmationCurrent && <p role="alert" className="text-ui text-state-failed">The browser page or control changed. Cancel and choose the action again.</p>}
        {error && <p role="alert" className="break-words text-ui text-state-failed">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction disabled={busy || !confirmationCurrent} onClick={(event) => { event.preventDefault(); confirm() }}>{confirmation?.action === 'close' ? 'Close page' : 'Reset session'}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
}

