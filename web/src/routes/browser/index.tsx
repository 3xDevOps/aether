import { useCallback, useEffect, useRef, useState } from 'react'
import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import type { DevBrowserNavigateParams, DevBrowserPage, DevBrowserStatusResult, DevController, DevControlFence, DevSurface } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { EvidenceDrawer } from '@/routes/terminal/evidence-drawer'
import { runTabPanel } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { BrowserSurface } from './surface'

// A tab-local identity survives pane detach but is never cloned through
// sessionStorage into another window. Reloads observe first, then take control.
let tabControlSession: string | undefined

function BrowserView({ params }: RouteProps) {
  const identity = useStore((state) => state.identityKey)
  return <BrowserRoute key={`${identity}:${params.runId}`} runID={params.runId} />
}

function BrowserRoute({ runID }: { runID: string }) {
  const run = useStore((state) => state.runs[runID])
  const members = useStore((state) => state.members)
  const navigate = useStore((state) => state.navigate)
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
  const alive = useRef(true)
  const reading = useRef(false)
  const mutation = useRef(0)
  const focused = useRef(!document.hidden)
  const owned = useRef<{ surface: DevSurface; fence: DevControlFence } | null>(null)
  const selectedPage = pages.find((page) => page.page_id === selected)
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

  useEffect(() => {
    alive.current = true
    void refresh()
    const timer = setInterval(() => { if (!document.hidden) void refresh() }, 1500)
    return () => { alive.current = false; clearInterval(timer) }
  }, [refresh])
  useEffect(() => {
    if (selectedPage) setAddress(selectedPage.url)
  }, [selectedPage?.url, selectedPage?.page_id])
  useEffect(() => {
    if (selectedPage) setPreset(`${selectedPage.width}x${selectedPage.height}`)
  }, [selectedPage?.width, selectedPage?.height, selectedPage?.page_id])

  const perform = async (operation: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    setError('')
    mutation.current++
    try { await operation() }
    catch (cause) { if (alive.current) setError(message(cause)) }
    finally {
      mutation.current++
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

  if (!run) return <MissingRun />
  const writable = Boolean(fence && !busy && !blocked && status?.running)
  const controllerName = controller ? controller.kind === 'run_agent' ? `Agent · ${controller.run_id}` : `Member · ${members[controller.member_id ?? '']?.display_name ?? controller.member_id}` : 'Nobody'

  return <div className="flex h-full min-h-0 min-w-0 flex-col">
    <RunHeader run={run} active="browser" />
    <section {...runTabPanel('browser', 'flex min-h-0 min-w-0 flex-1 flex-col gap-2 overflow-y-auto p-2 sm:p-3')}>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span role="status">Browser: {status?.state ?? 'Checking'}{status?.session_id ? ` · ${status.session_id}` : ''}</span>
        <span>{owns ? 'You control this browser' : 'Watch mode'} · Controller: {controllerName}{controller?.expires_at ? ` · expires ${new Date(controller.expires_at).toLocaleTimeString()}` : ''}</span>
        {surface && (!owns || blocked) && <Button size="sm" variant="outline" disabled={busy} onClick={() => acquire(false)}>Acquire control</Button>}
        {surface && controller && !owns && <Button size="sm" variant="outline" disabled={busy} onClick={() => acquire(true)}>Take over browser</Button>}
        {surface && fence && <Button size="sm" variant="outline" disabled={busy} onClick={() => void perform(() => release())}>Release control</Button>}
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => void perform(async () => { await refresh(); setConnection((value) => value + 1); setBlocked(false) })}>Reconnect</Button>
        <Button size="sm" variant="ghost" onClick={() => navigate('terminal', { runId: runID })}>Hide browser</Button>
      </div>
      {status?.reason && <p role="alert" className="break-words text-sm text-destructive">{status.reason}</p>}
      {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
      <form className="flex min-w-0 flex-wrap gap-1" onSubmit={(event) => { event.preventDefault(); if (selectedPage) navigatePage('url'); else open() }}>
        <Button type="button" size="sm" variant="outline" aria-label="Back" disabled={!writable || !selectedPage} onClick={() => navigatePage('back')}>Back</Button>
        <Button type="button" size="sm" variant="outline" aria-label="Forward" disabled={!writable || !selectedPage} onClick={() => navigatePage('forward')}>Forward</Button>
        <Button type="button" size="sm" variant="outline" disabled={!writable || !selectedPage} onClick={() => navigatePage('reload')}>Reload page</Button>
        <input aria-label="Browser URL" type="url" value={address} onChange={(event) => setAddress(event.target.value)} className="min-w-32 flex-1 rounded border border-input bg-background px-2 py-1 text-base sm:text-sm" />
        {selectedPage && <Button type="submit" size="sm" disabled={!writable}>Go</Button>}
        <Button type="button" size="sm" disabled={busy || !status?.available || Boolean(status.session_id && !fence)} onClick={open}>{pages.length ? 'New page' : 'Open browser'}</Button>
      </form>
      <div className="flex flex-wrap items-center gap-2">
        <label className="flex min-w-0 items-center gap-1 text-sm">Page
          <select aria-label="Browser page" value={selected} disabled={!writable} className="min-w-0 max-w-64 rounded border border-input bg-background p-1" onChange={(event) => {
            const page = pages.find((item) => item.page_id === event.target.value)
            if (!page || !fence) return
            void perform(async () => { await api.devBrowserAction({ run_id: runID, session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision, ...fence, action: 'select' }); setSelected(page.page_id) })
          }}>
            {!pages.length && <option value="">No open pages</option>}
            {pages.map((page) => <option key={page.page_id} value={page.page_id}>{page.title || page.url || page.page_id}</option>)}
          </select>
        </label>
        <label className="flex items-center gap-1 text-sm">Viewport
          <select aria-label="Browser viewport" value={preset} className="rounded border border-input bg-background p-1" disabled={busy || Boolean(selectedPage && !fence)} onChange={(event) => {
            const value = event.target.value
            if (!selectedPage) { setPreset(value); return }
            if (!fence) return
            const [width, height] = value.split('x').map(Number)
            void perform(async () => { const result = await api.devBrowserViewport({ run_id: runID, session_id: selectedPage.session_id, page_id: selectedPage.page_id, page_revision: selectedPage.page_revision, ...fence, width, height }); updatePage(result.page) })
          }}>
            <option value="1280x800">Desktop · 1280 × 800</option>
            <option value="390x844">Phone · 390 × 844</option>
            <option value="844x390">Phone landscape · 844 × 390</option>
            {!['1280x800', '390x844', '844x390'].includes(preset) && <option value={preset}>Current · {preset.replace('x', ' × ')}</option>}
          </select>
        </label>
        <Button size="sm" variant="outline" disabled={!selectedPage || busy} onClick={() => {
          if (!selectedPage) return
          void perform(async () => { const result = await api.devBrowserScreenshot({ run_id: runID, session_id: selectedPage.session_id, page_id: selectedPage.page_id, page_revision: selectedPage.page_revision }); setCapture(result.artifact.id) })
        }}>Screenshot</Button>
        <EvidenceDrawer runID={runID} workspaceID={run.workspace_id} />
        <Button size="sm" variant="outline" disabled={!writable || !selectedPage} onClick={() => {
          if (!selectedPage || !fence || !window.confirm('Close this shared page for everyone? Other pages and the browser session remain.')) return
          void perform(async () => { await api.devBrowserClose({ run_id: runID, session_id: selectedPage.session_id, page_id: selectedPage.page_id, page_revision: selectedPage.page_revision, ...fence }) })
        }}>Close page</Button>
        <Button size="sm" variant="outline" disabled={!surface || !fence || busy} onClick={() => {
          if (!status?.session_id || !fence || !window.confirm('Reset the shared browser session? All pages, cookies and logins will be lost.')) return
          void perform(async () => { await api.devBrowserReset({ run_id: runID, session_id: status.session_id!, ...fence }); setPages([]); setController(null); setBlocked(false) })
        }}>Reset session</Button>
      </div>
      {capture && <p role="status" className="break-words text-xs text-muted-foreground">Captured {capture}. Open Evidence and explicitly select captures to retain; nothing has been published.</p>}
      {selectedPage ? <BrowserSurface key={`${selectedPage.session_id}:${selectedPage.page_id}`} runID={runID} page={selectedPage} control={!busy && !blocked ? fence : null} connection={connection}
        onPage={updatePage} onError={(text) => { setError(text); setBlocked(true); void release() }} /> : <p className="p-4 text-sm text-muted-foreground">Opening this pane only observes existing state. Use Open browser to start a page. Hiding the pane does not stop the app or clear its login.</p>}
    </section>
  </div>
}

registerRoute('browser', BrowserView)
