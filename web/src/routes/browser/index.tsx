import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowLeft, ArrowRight, Bot, Bug, Columns2, Ellipsis, Keyboard, MessageSquarePlus, MonitorSmartphone, MousePointer2, RotateCw, X } from '@/components/icons'
import { MissingRun } from '@/components/missing-run'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Menu, MenuContent, MenuItem, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Spinner } from '@/components/ui/spinner'
import { api, ApiError } from '@/lib/api'
import { message } from '@/lib/format'
import { coarsePointer, useDelayed, useElementSize, useMediaQuery } from '@/lib/hooks'
import { shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { keyboardBusy, onTabListKeyDown } from '@/lib/keys'
import { memberName, resolveMember } from '@/lib/member-name'
import type { DevBrowserCloseParams, DevBrowserPage, DevBrowserResetParams, DevBrowserStatusResult, DevController, DevControlFence, DevSurface } from '@/lib/types'
import { cn } from '@/lib/utils'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { codeConflict } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { isTerminal } from '@/store/runs'
import { normalizeAddress } from './address'
import { AddressBar } from './address-bar'
import { AnnotateLayer } from './annotate'
import { addNote, closeNotes } from './feedback'
import { FeedbackBar } from './feedback-bar'
import { LogStrip, useBrowserLogs } from './logs'
import { OpenAPage, PortsControl, usePorts } from './ports'
import { BrowserSurface, type BrowserPick, type BrowserSurfaceHandle } from './surface'
import { paneViewport, viewportPresets } from './viewport'

// A tab-local identity survives pane detach but is never cloned through
// sessionStorage into another window. A reload starts a new one.
let tabControlSession: string | undefined

const fitDelay = 250
const compactBelow = 480

const stateTitles: Record<string, string> = {
  creating: 'Starting the browser',
  paused: 'Browser paused',
  session_lost: 'Browser connection lost',
}

interface Lease { surface: DevSurface; fence: DevControlFence }
interface Notice { tone: 'failed' | 'info'; text: string }
type Confirmation =
  | { action: 'close'; target: DevBrowserCloseParams }
  | { action: 'reset'; target: DevBrowserResetParams }

/** Present on a frame wide enough to show the Browser beside another view. */
export interface BrowserSplit {
  beside: boolean
  /** The view it sits beside. */
  view: string
  toggle: () => void
}

function browserSurface(session: string): DevSurface {
  return { kind: 'browser', id: 'browser', incarnation: session }
}

// A lease read from the server carries more than the two fields a mutation takes.
function fenceOf(control: DevControlFence): DevControlFence {
  return { control_session_id: control.control_session_id, control_generation: control.control_generation }
}

function isConflict(cause: unknown): boolean {
  return cause instanceof ApiError && cause.code === codeConflict
}

function pageLabel(page: DevBrowserPage): string {
  if (page.title) return page.title
  return page.url && page.url !== 'about:blank' ? page.url.replace(/^https?:\/\//, '') : 'New page'
}

interface BrowserViewProps {
  runID: string
  split?: BrowserSplit
  /** Whom notes on the page are sent to, under the Session composer's rules. */
  agent: AgentTerminal
  agentName: string
  onCaptures: (opener: HTMLElement | null) => void
}

export function BrowserView(props: BrowserViewProps) {
  const identity = useStore((state) => state.identityKey)
  return <BrowserRoute key={`${identity}:${props.runID}`} {...props} />
}

function BrowserRoute({ runID, split, agent, agentName, onCaptures }: BrowserViewProps) {
  const run = useStore((state) => state.runs[runID])
  const cap = useCapability()
  const coarse = useMediaQuery(coarsePointer)
  const members = useStore((state) => state.members)
  const mergedInto = useStore((state) => state.mergedInto)
  const self = useStore((state) => state.info?.member.id)
  const preset = useStore((state) => state.browserPresets[runID])
  const setPreset = useStore((state) => state.setBrowserPreset)
  const request = useStore((state) => state.browserRequests[runID])
  const requestBrowser = useStore((state) => state.requestBrowser)
  const [controlSession] = useState(() => tabControlSession ??= crypto.randomUUID())
  const [status, setStatus] = useState<DevBrowserStatusResult | null>(null)
  const [readError, setReadError] = useState('')
  const [pages, setPages] = useState<DevBrowserPage[]>([])
  const [selected, setSelected] = useState('')
  const [unseen, setUnseen] = useState<string[]>([])
  const [controller, setController] = useState<DevController | null>(null)
  const [lease, setLease] = useState<Lease | null>(null)
  const [pending, setPending] = useState<'load' | 'work' | null>(null)
  const [going, setGoing] = useState('')
  const [resizing, setResizing] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const [focusedTab, setFocusedTab] = useState(0)
  const [annotating, setAnnotating] = useState(false)
  // Why this browser session cannot inspect, in its server's words.
  const [refused, setRefused] = useState<{ session: string; reason: string } | null>(null)
  const [logsOpen, setLogsOpen] = useState(false)
  const [seenLog, setSeenLog] = useState<Record<string, number>>({})
  const pick = useRef<BrowserPick>(null)
  const annotateToggle = useRef<HTMLButtonElement>(null)
  const [paneRef, pane] = useElementSize<HTMLDivElement>()
  const address = useRef<HTMLInputElement>(null)
  const surfaceHandle = useRef<BrowserSurfaceHandle>(null)
  const menuTrigger = useRef<HTMLButtonElement>(null)
  // Set by a menu item that moves focus itself once its work is done.
  const leavingMenu = useRef(false)
  const inFlight = useRef(false)
  const fitting = useRef(false)
  const alive = useRef(true)
  const reading = useRef(false)
  const mutation = useRef(0)
  const focused = useRef(!document.hidden)
  // True from focus or a key landing elsewhere in the dashboard until the
  // Browser is used again.
  const away = useRef(false)
  const held = useRef<Lease | null>(null)
  const acquiring = useRef<Promise<DevControlFence | null> | null>(null)
  const queued = useRef<{ typed: string; settle: (opened: boolean) => void } | null>(null)
  const known = useRef<Set<string> | null>(null)

  const page = pages.find((item) => item.page_id === selected) ?? null
  const selectedTab = Math.max(0, pages.findIndex((item) => item.page_id === selected))
  const surface = status?.session_id ? browserSurface(status.session_id) : null
  const other = controller && controller.control_session_id !== controlSession ? controller : null
  const fence = lease?.fence ?? null
  const chosen = preset ? viewportPresets[preset] : undefined
  const target = chosen ?? paneViewport(pane)
  const driverMember = resolveMember({ members, mergedInto }, other?.member_id ?? '').member
  const driverName = driverMember ? memberName({ members, mergedInto }, other?.member_id ?? '') : 'Another member'
  const driver = !other ? ''
    : other.kind === 'run_agent' ? 'The agent is driving'
      : other.member_id === self ? 'You are driving in another tab'
        : `${driverName} is driving`
  const latest = useRef({ surface, other, driver, page, status, target })
  latest.current = { surface, other, driver, page, status, target }
  const logs = useBrowserLogs(runID, page)
  const newestLog = logs.entries.at(-1)?.sequence ?? 0
  const logKey = page ? `${page.session_id}:${page.page_id}` : ''
  const unseenLogs = logsOpen ? 0 : logs.entries.filter((entry) => entry.sequence > (seenLog[logKey] ?? 0)).length
  const unannotatable = !cap.hasMethod('dev.browser.inspect')
    ? 'This server does not serve dev.browser.inspect, so a page cannot be annotated. Update aether-server.'
    : refused && refused.session === status?.session_id ? refused.reason : ''

  const hold = (next: Lease | null) => {
    held.current = next
    setLease(next)
  }
  // A lease is kept while the window has focus and the Browser is in use, or
  // an action that began there is still running.
  const wanted = () => alive.current && focused.current && (!away.current || inFlight.current)

  const release = useCallback(async (given: Lease | null = held.current) => {
    if (!given) return
    held.current = null
    mutation.current++
    if (alive.current) {
      setLease(null)
      setController(null)
    }
    try {
      await api.devControlRelease({ run_id: runID, surface: given.surface, ...given.fence })
    } catch (cause) {
      // Stale means the lease was already taken or revoked, which is the aim.
      if (alive.current && !isConflict(cause)) setNotice({ tone: 'failed', text: message(cause) })
    }
  }, [runID])

  // A lease held here never expires and keeps the agent out, so it is given
  // up as soon as the Browser is not what is being used: the window loses
  // focus, or focus or a key lands elsewhere in the dashboard. A menu or a
  // dialog may be the Browser's own, so those are left alone.
  useEffect(() => {
    focused.current = !document.hidden
    const blur = () => { focused.current = false; void release() }
    const focus = () => { focused.current = !document.hidden }
    const visibility = () => { if (document.hidden) blur(); else focus() }
    const elsewhere = (event: Event) => {
      if (!(event.target instanceof Element) || event.target.closest('[data-browser], [role="menu"], [role="dialog"], [role="alertdialog"]')) return
      away.current = true
      // An action still running keeps the lease it works under until it ends.
      if (!inFlight.current) void release()
    }
    // A wheel or a touch on the page moves no focus, and neither does a
    // click on a button in Safari.
    const back = (event: Event) => {
      if (event.target instanceof Element && event.target.closest('[data-browser]')) away.current = false
    }
    const uses = ['focusin', 'pointerdown', 'wheel']
    window.addEventListener('blur', blur)
    window.addEventListener('focus', focus)
    document.addEventListener('visibilitychange', visibility)
    document.addEventListener('focusin', elsewhere)
    document.addEventListener('keydown', elsewhere)
    for (const type of uses) document.addEventListener(type, back, { capture: true, passive: true })
    return () => {
      window.removeEventListener('blur', blur)
      window.removeEventListener('focus', focus)
      document.removeEventListener('visibilitychange', visibility)
      document.removeEventListener('focusin', elsewhere)
      document.removeEventListener('keydown', elsewhere)
      for (const type of uses) document.removeEventListener(type, back, true)
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
      const session = next.session_id ? browserSurface(next.session_id) : null
      const [inventory, ownership] = await Promise.all([
        next.running && next.session_id ? api.devBrowserPages({ run_id: runID, session_id: next.session_id }) : null,
        session ? api.devControlStatus({ run_id: runID, surface: session }) : null,
      ])
      if (!alive.current || version !== mutation.current) return
      const shown = inventory?.selected_page_id ?? next.selected_page_id ?? ''
      const ids = (inventory?.pages ?? []).map((item) => item.page_id)
      const arrived = known.current ? ids.filter((id) => id !== shown && !known.current!.has(id)) : []
      known.current = new Set(ids)
      setStatus(next)
      setReadError('')
      setPages(inventory?.pages ?? [])
      setSelected(shown)
      if (arrived.length) setUnseen((previous) => [...previous, ...arrived])
      const holder = ownership?.controller ?? null
      const mine = session && holder?.control_session_id === controlSession ? { surface: session, fence: fenceOf(holder) } : null
      if (mine && !wanted()) void release(mine)
      else {
        setController(holder)
        held.current = mine
        setLease((previous) => previous?.surface.incarnation === mine?.surface.incarnation && previous?.fence.control_generation === mine?.fence.control_generation ? previous : mine)
      }
    } catch (cause) {
      if (alive.current) setReadError(message(cause))
    } finally { reading.current = false }
  }, [runID, controlSession, release])

  const ended = run ? isTerminal(run.status) : false
  const ports = usePorts(runID, !ended)
  useEffect(() => {
    alive.current = true
    void refresh()
    const timer = ended ? undefined : setInterval(() => { if (!document.hidden) void refresh() }, 1500)
    return () => { alive.current = false; clearInterval(timer) }
  }, [refresh, ended])
  useEffect(() => {
    setUnseen((previous) => (previous.includes(selected) ? previous.filter((id) => id !== selected) : previous))
  }, [selected])
  useEffect(() => setFocusedTab(selectedTab), [selectedTab])
  const paged = page !== null
  useEffect(() => {
    if (!paged) setAnnotating(false)
  }, [paged])
  useEffect(() => {
    if (logsOpen && logKey) setSeenLog((seen) => (seen[logKey] === newestLog ? seen : { ...seen, [logKey]: newestLog }))
  }, [logsOpen, logKey, newestLog])

  const acquire = async (given: DevSurface, takeover: boolean, expected: number): Promise<DevControlFence | null> => {
    mutation.current++
    try {
      const result = await api.devControlAcquire({ run_id: runID, surface: given, control_session_id: controlSession, expected_generation: expected, takeover })
      if (!result.controller) return null
      const next = { surface: given, fence: fenceOf(result.controller) }
      if (!wanted()) {
        await release(next)
        return null
      }
      hold(next)
      setController(result.controller)
      return next.fence
    } catch (cause) {
      if (!alive.current) return null
      // Losing a free lease to someone quicker is not an error: the next read names them.
      if (takeover || !isConflict(cause)) setNotice({ tone: 'failed', text: message(cause) })
      void refresh()
      return null
    } finally { mutation.current++ }
  }

  /** The lease, taken first when nobody holds it. Null when someone else does. */
  const ensureControl = (): Promise<DevControlFence | null> => {
    if (held.current) return Promise.resolve(held.current.fence)
    const now = latest.current
    if (now.other) setNotice({ tone: 'info', text: `${now.driver}. Take over to use the page.` })
    if (now.other || !now.surface) return Promise.resolve(null)
    return acquiring.current ??= acquire(now.surface, false, 0).finally(() => { acquiring.current = null })
  }

  const perform = async (operation: () => Promise<void>, kind: 'load' | 'work' = 'work') => {
    if (inFlight.current) return
    inFlight.current = true
    setPending(kind)
    setNotice(null)
    mutation.current++
    try {
      await surfaceHandle.current?.lift()
      await operation()
    } catch (cause) { if (alive.current) setNotice({ tone: 'failed', text: message(cause) }) }
    finally {
      mutation.current++
      // An action that outlasted its user's stay gives the lease back.
      if (away.current) await release()
      inFlight.current = false
      if (alive.current) {
        setPending(null)
        if (!queued.current) await refresh()
      }
    }
  }
  const drive = (operation: (held: DevControlFence) => Promise<void>, kind?: 'load' | 'work') => perform(async () => {
    const taken = await ensureControl()
    if (taken) await operation(taken)
  }, kind)
  const updatePage = (next: DevBrowserPage) => {
    mutation.current++
    setPages((previous) => previous.map((item) => item.page_id === next.page_id && item.session_id === next.session_id && item.page_revision <= next.page_revision ? next : item))
  }

  const openPage = async (url: string): Promise<boolean> => {
    const now = latest.current
    const session = now.status?.session_id
    const taken = session ? await ensureControl() : { control_session_id: controlSession, control_generation: 0 }
    if (!taken) return false
    const result = await api.devBrowserOpen({ run_id: runID, session_id: session, ...taken, url, width: now.target?.width, height: now.target?.height })
    const next = { surface: browserSurface(result.page.session_id), fence: fenceOf(result.control) }
    if (!wanted()) {
      await release(next)
      return true
    }
    hold(next)
    setStatus((previous) => previous && { ...previous, running: true, state: 'running', session_id: result.page.session_id })
    setPages((previous) => [...previous.filter((item) => item.page_id !== result.page.page_id), result.page])
    setSelected(result.page.page_id)
    return true
  }
  const go = async (typed: string): Promise<boolean> => {
    const url = normalizeAddress(typed)
    if (!url || !latest.current.status?.available) return false
    if (inFlight.current) {
      // Enter during another action waits for it; only the latest one waits.
      queued.current?.settle(false)
      return new Promise((settle) => { queued.current = { typed, settle } })
    }
    let opened = false
    setGoing(url)
    await perform(async () => {
      const shown = latest.current.page
      if (!shown) opened = await openPage(url)
      else {
        const taken = await ensureControl()
        if (!taken) return
        const result = await api.devBrowserNavigate({ run_id: runID, session_id: shown.session_id, page_id: shown.page_id, page_revision: shown.page_revision, ...taken, direction: 'url', url, timeout_ms: 15000 })
        updatePage(result.page)
        opened = true
      }
    }, 'load')
    if (!alive.current) return false
    setGoing((current) => (current === url ? '' : current))
    return opened
  }
  const focusPage = () => {
    // Focus that has moved on since Enter stays where it went.
    if (document.activeElement !== address.current) return
    address.current?.blur()
    if (!window.matchMedia?.(coarsePointer).matches) requestAnimationFrame(() => surfaceHandle.current?.focus())
  }
  // A port picked in the pane goes the way of an address typed and entered:
  // the field takes focus and hands it to the page once the port is open.
  const openPort = (typed: string) => {
    if (!window.matchMedia?.(coarsePointer).matches) address.current?.focus()
    void go(typed).then((opened) => { if (opened) focusPage() })
  }
  const navigate = (direction: 'back' | 'forward' | 'reload') => {
    if (!page) return
    void drive(async (taken) => {
      const result = await api.devBrowserNavigate({ run_id: runID, session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision, ...taken, direction, timeout_ms: 15000 })
      updatePage(result.page)
    }, 'load')
  }
  const selectPage = (next: DevBrowserPage) => void drive(async (taken) => {
    await api.devBrowserAction({ run_id: runID, session_id: next.session_id, page_id: next.page_id, page_revision: next.page_revision, ...taken, action: 'select' })
    setSelected(next.page_id)
  })
  const choosePreset = async (value: string) => {
    if (latest.current.surface && !(await ensureControl())) return
    setPreset(runID, value === 'fit' ? null : value)
  }
  const takeOver = () => {
    if (surface && other) void perform(async () => { await acquire(surface, true, other.control_generation) })
  }
  const screenshot = () => {
    if (!page) return
    void perform(async () => {
      const { artifact } = await api.devBrowserScreenshot({ run_id: runID, session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision })
      // Its card is where it gets words, goes to the agent, or is kept as evidence.
      addNote(runID, { kind: 'screenshot', id: artifact.id, body: '', page: { url: page.url, width: artifact.width, height: artifact.height }, artifact })
      setAnnotating(true)
    })
  }
  const annotate = (on: boolean) => {
    if (on && unannotatable) setNotice({ tone: 'failed', text: unannotatable })
    if (on && (!page || unannotatable)) return
    // Leaving keeps what an open card holds and drops one left empty.
    if (!on) closeNotes(runID)
    // A press the page still holds could not be released once the pointer picks.
    else void surfaceHandle.current?.lift()
    setAnnotating(on)
    // The keyboard leaves the page with the pointer: a key typed while picking is not for the app.
    if (on) annotateToggle.current?.focus()
  }

  const newPage = () => {
    if (inFlight.current) return
    leavingMenu.current = true
    void perform(async () => {
      let opened = false
      try { opened = await openPage('') }
      finally { if (!away.current) (opened ? address : menuTrigger).current?.focus() }
    }, 'load')
  }

  const ask = async (action: Confirmation['action']) => {
    setNotice(null)
    const taken = await ensureControl()
    const now = latest.current
    const session = now.status?.session_id
    if (!alive.current) return
    if (taken && session && action === 'reset') setConfirmation({ action, target: { run_id: runID, session_id: session, ...taken } })
    else if (taken && now.page && now.status?.running) setConfirmation({ action: 'close', target: { run_id: runID, session_id: now.page.session_id, page_id: now.page.page_id, page_revision: now.page.page_revision, ...taken } })
    else if (!away.current) menuTrigger.current?.focus()
  }
  const confirmationCurrent = Boolean(confirmation && fence
    && status?.session_id === confirmation.target.session_id
    && fence.control_session_id === confirmation.target.control_session_id
    && fence.control_generation === confirmation.target.control_generation
    && (confirmation.action === 'reset' || (status.running
      && page?.session_id === confirmation.target.session_id
      && page.page_id === confirmation.target.page_id
      && page.page_revision === confirmation.target.page_revision)))
  const confirm = () => {
    if (!confirmation || !confirmationCurrent || !focused.current) return
    const captured = confirmation
    void perform(async () => {
      if (captured.action === 'close') await api.devBrowserClose(captured.target)
      else {
        const fresh = await api.devBrowserReset(captured.target)
        setStatus((previous) => previous && { ...previous, session_id: fresh.session_id, selected_page_id: undefined })
        setPages([])
        setController(null)
        hold(null)
      }
      setConfirmation(null)
    })
  }

  useEffect(() => {
    if (!fence || !page || !target || (page.width === target.width && page.height === target.height)) return
    let timer = setTimeout(function apply() {
      if (inFlight.current || fitting.current || (surfaceHandle.current && !surfaceHandle.current.idle())) {
        timer = setTimeout(apply, fitDelay)
        return
      }
      fitting.current = true
      setResizing(true)
      api.devBrowserViewport({ run_id: runID, session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision, ...fence, width: target.width, height: target.height })
        .then((result) => { if (alive.current) updatePage(result.page) }, (cause: unknown) => {
          if (!alive.current) return
          // A stale revision is retried by the read it triggers.
          if (!isConflict(cause)) setNotice({ tone: 'failed', text: message(cause) })
          void refresh()
        })
        .finally(() => {
          fitting.current = false
          if (alive.current) setResizing(false)
        })
    }, fitDelay)
    return () => clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fence?.control_generation, page?.page_id, page?.page_revision, page?.width, page?.height, target?.width, target?.height])

  // Run from an effect so the queued address sees the page and session the
  // finished action left, which an open or a navigation has only just set.
  useEffect(() => {
    const next = queued.current
    if (pending || !next) return
    queued.current = null
    void go(next.typed).then(next.settle)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pending])
  // What was asked of this Browser from outside it: an address from
  // openRunLink, or '' for the address bar alone.
  useEffect(() => {
    if (request === undefined || !status) return
    requestBrowser(runID, null)
    if (request) void go(request)
    else address.current?.focus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [request, status === null])

  const empty = Boolean(status?.available) && !page
  useEffect(() => {
    if (empty && !split?.beside && !window.matchMedia?.(coarsePointer).matches) address.current?.focus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [empty])

  const chord = (act: () => void) => (event: KeyboardEvent) => {
    // The page must not also receive a key the toolbar acted on.
    event.preventDefault()
    event.stopPropagation()
    act()
  }
  useKeybindings('browser', {
    'browser-reload': chord(() => navigate('reload')),
    'browser-back': chord(() => navigate('back')),
    'browser-forward': chord(() => navigate('forward')),
    'browser-annotate': chord(() => annotate(!annotating)),
  })
  // Escape leaves a run, and the table gives a key to one binding. While a
  // page is annotated this answers it first, wherever in the run focus is;
  // a card, a menu or a dialog keeps its own Escape.
  useEffect(() => {
    if (!annotating) return
    const leave = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || keyboardBusy(event)) return
      event.preventDefault()
      annotate(false)
    }
    window.addEventListener('keydown', leave, true)
    return () => window.removeEventListener('keydown', leave, true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [annotating])

  const waiting = useDelayed(!status && !readError)
  if (!run) return <MissingRun />
  const loading = pending === 'load'
  const compact = pane.width > 0 && pane.width < compactBelow
  // A phone's row has no room for one more 44px button: Annotate starts from
  // Browser actions there, and while it is on its toggle takes the place of
  // Keyboard, which a page being annotated has no use for.
  const tight = compact && coarse
  const shownNotice = confirmation ? null : notice ?? (status && readError ? { tone: 'failed' as const, text: readError } : null)
  const ViewportIcon = chosen?.Icon ?? MonitorSmartphone
  const viewportItems = (
    <MenuRadioGroup value={chosen ? preset : 'fit'} onValueChange={(value) => void choosePreset(value)}>
      <MenuRadioItem value="fit">Fit the pane</MenuRadioItem>
      {Object.entries(viewportPresets).map(([id, size]) => (
        <MenuRadioItem key={id} value={id}>
          {size.label}
          <span className="ml-auto pl-4 text-ui-sm text-muted tabular-nums">{size.width} × {size.height}</span>
        </MenuRadioItem>
      ))}
    </MenuRadioGroup>
  )

  return <div data-browser className="flex h-full min-h-0 min-w-0 flex-col">
    <div role="toolbar" aria-label="Browser" className="@container relative flex shrink-0 flex-wrap items-center gap-1 border-b border-seam bg-chrome px-2 py-1">
      <Button variant="ghost" size="icon" label="Back" hint={`Back (${shortcutLabel('browser-back')})`} aria-disabled={!page || undefined} onClick={() => navigate('back')}><ArrowLeft /></Button>
      <Button variant="ghost" size="icon" label="Forward" hint={`Forward (${shortcutLabel('browser-forward')})`} aria-disabled={!page || undefined} onClick={() => navigate('forward')}><ArrowRight /></Button>
      <Button variant="ghost" size="icon" label={loading ? 'Loading' : 'Reload'} hint={loading ? 'Loading' : `Reload (${shortcutLabel('browser-reload')})`} aria-disabled={!page || loading || undefined} onClick={() => navigate('reload')}>
        {loading ? <Spinner className="size-4" /> : <RotateCw />}
      </Button>
      <AddressBar url={page && page.url !== 'about:blank' ? page.url : ''} going={going} disabled={!status?.available} ports={page ? ports?.list ?? [] : []} input={address} onSubmit={go} onOpened={focusPage} />
      {!compact && <PortsControl runID={runID} ports={ports} available={Boolean(status?.available)} onOpen={openPort} />}
      {!(tight && annotating) && <Button variant="ghost" size="icon" label="Keyboard" className="hidden coarse:inline-flex" aria-disabled={!page || undefined} onClick={() => surfaceHandle.current?.focus()}><Keyboard /></Button>}
      {(!tight || annotating) && (
        <Button ref={annotateToggle} variant={annotating ? 'secondary' : 'ghost'} size="icon" label="Annotate" hint={unannotatable || `Annotate for the agent (${shortcutLabel('browser-annotate')})`}
          aria-pressed={annotating} aria-disabled={!page || (unannotatable !== '' && !annotating) || undefined} onClick={() => annotate(!annotating)}><MessageSquarePlus /></Button>
      )}
      {!compact && (
        <Button variant={logsOpen ? 'secondary' : 'ghost'} size="icon" label="Console and network" hint={unseenLogs ? `Console and network: ${unseenLogs} new` : 'Console and network'}
          aria-pressed={logsOpen} aria-disabled={!page || undefined} className="relative" onClick={() => { if (page) setLogsOpen(!logsOpen) }}>
          <Bug />
          {unseenLogs > 0 && <span aria-hidden className="absolute -top-1 -right-1 min-w-4 rounded-full bg-state-failed px-1 text-ui-xs leading-4 font-medium text-on-failed tabular-nums">{unseenLogs}</span>}
        </Button>
      )}
      {!compact && (
        <Menu>
          <MenuTrigger asChild><Button variant="ghost" size="icon" label="Viewport"><ViewportIcon /></Button></MenuTrigger>
          <MenuContent align="end">{viewportItems}</MenuContent>
        </Menu>
      )}
      {split && (
        <Button variant="ghost" size="icon" label={split.beside ? 'Show the Browser as a tab' : `Show beside ${split.view}`} onClick={split.toggle}><Columns2 /></Button>
      )}
      <Menu>
        <MenuTrigger asChild>
          <Button ref={menuTrigger} variant="ghost" size="icon" label="Browser actions" className="relative">
            <Ellipsis />
            {compact && unseenLogs > 0 && <span aria-hidden className="absolute top-1 right-1 size-1.5 rounded-full bg-state-failed" />}
          </Button>
        </MenuTrigger>
        <MenuContent
          align="end"
          onCloseAutoFocus={(event) => {
            if (!leavingMenu.current) return
            leavingMenu.current = false
            event.preventDefault()
          }}
        >
          {compact && <>
            <MenuLabel>Viewport</MenuLabel>
            {viewportItems}
            <MenuSeparator />
          </>}
          {tight && !annotating && <MenuItem disabled={!page} onSelect={() => annotate(true)}>Annotate</MenuItem>}
          <MenuItem disabled={!status?.available} onSelect={newPage}>New page</MenuItem>
          <MenuItem disabled={!page} onSelect={screenshot}>Screenshot</MenuItem>
          {compact && <MenuItem disabled={!page} onSelect={() => setLogsOpen(!logsOpen)}>{logsOpen ? 'Hide console and network' : unseenLogs ? `Console and network (${unseenLogs} new)` : 'Console and network'}</MenuItem>}
          {lease && <MenuItem onSelect={() => void release()}>Release control</MenuItem>}
          <MenuSeparator />
          <MenuItem disabled={!page} onSelect={() => { leavingMenu.current = true; void ask('close') }}>Close page…</MenuItem>
          <MenuItem tone="danger" disabled={!surface} onSelect={() => { leavingMenu.current = true; void ask('reset') }}>Reset browser…</MenuItem>
        </MenuContent>
      </Menu>
      {lease && (
        <span className="flex shrink-0 items-center gap-1 px-1 text-ui-sm text-muted">
          <MousePointer2 role="img" aria-label="You are driving" className="size-3.5 text-accent" />
          <span aria-hidden className="hidden @2xl:inline">You are driving</span>
        </span>
      )}
      {other && (
        <div role="status" className="flex min-w-0 basis-full items-center gap-2 px-1 @2xl:basis-auto">
          {other.kind === 'run_agent'
            ? <Bot aria-hidden className="size-3.5 shrink-0 text-muted" />
            : <Avatar name={driverName} color={driverMember?.color} />}
          <span className="min-w-0 flex-1 truncate text-ui-sm text-muted">{driver}</span>
          <Button variant="secondary" size="sm" aria-disabled={pending !== null || undefined} onClick={takeOver}>Take over</Button>
        </div>
      )}
    </div>
    <FeedbackBar run={run} agent={agent} agentName={agentName} />
    {pages.length > 1 && (
      <div role="tablist" aria-label="Pages" className="flex shrink-0 items-center gap-0.5 overflow-x-auto border-b border-seam bg-chrome px-2 py-0.5">
        {pages.map((item, index) => {
          const fresh = unseen.includes(item.page_id)
          return (
            <Button
              key={item.page_id}
              role="tab"
              variant={item.page_id === selected ? 'secondary' : 'ghost'}
              size="sm"
              aria-selected={item.page_id === selected}
              aria-label={fresh ? `${pageLabel(item)} (new)` : undefined}
              tabIndex={index === focusedTab ? 0 : -1}
              onFocus={() => setFocusedTab(index)}
              onKeyDown={(event) => onTabListKeyDown(event, pages.length, focusedTab, setFocusedTab)}
              onClick={() => { if (item.page_id !== selected) selectPage(item) }}
            >
              {fresh && <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-accent" />}
              <span className="max-w-48 truncate">{pageLabel(item)}</span>
            </Button>
          )
        })}
      </div>
    )}
    <div ref={paneRef} className="relative min-h-0 min-w-0 flex-1">
      {page ? (
        <BrowserSurface key={`${page.session_id}:${page.page_id}`} ref={surfaceHandle} runID={runID} page={page} control={fence}
          free={!other} acquire={ensureControl} paused={pending !== null || resizing} pick={annotating && !unannotatable ? pick : undefined}
          onBlocked={() => setNotice({ tone: 'info', text: `${driver}. Take over to use the page.` })}
          onError={(text) => { setNotice({ tone: 'failed', text }); void refresh() }}
          onNavigated={() => void refresh()} onPage={updatePage} />
      ) : (
        <div className="h-full overflow-y-auto">
          {!status ? (readError
            ? <EmptyState title="Browser unavailable"><span role="alert" className="break-words">{readError}</span></EmptyState>
            : waiting && <div className="grid h-full place-items-center"><Spinner label="Checking the browser" className="size-5" /></div>
          ) : loading || status.state === 'creating' ? (
            <EmptyState title={<span className="inline-flex items-center gap-2"><Spinner />{stateTitles.creating}</span>}>The first page takes longer: the server is starting a browser beside the session.</EmptyState>
          ) : status.available ? (
            <OpenAPage runID={runID} ports={ports} onOpen={openPort} />
          ) : (
            <EmptyState
              title={stateTitles[status.state] ?? 'Browser unavailable'}
              action={surface && status.state !== 'paused' && <Button variant="secondary" onClick={() => void ask('reset')}>Reset browser…</Button>}
            >
              {status.reason && <span role="alert" className="break-words">{status.reason}</span>}
            </EmptyState>
          )}
        </div>
      )}
      {annotating && page && (
        <AnnotateLayer key={`notes:${page.session_id}:${page.page_id}`} ref={pick} runID={runID} page={page} surface={surfaceHandle} picking={!unannotatable}
          onUnavailable={(reason) => {
            setRefused({ session: page.session_id, reason })
            annotate(false)
            setNotice({ tone: 'failed', text: reason })
          }}
          onNotice={(text) => setNotice({ tone: 'info', text })} onCaptures={onCaptures} />
      )}
      {shownNotice && (
        <div role={shownNotice.tone === 'failed' ? 'alert' : 'status'} className={cn('absolute inset-x-2 top-2 flex items-start gap-2 rounded-panel border border-seam bg-raised px-3 py-1.5 text-ui shadow-overlay', shownNotice.tone === 'failed' ? 'text-state-failed' : 'text-text')}>
          <span className="min-w-0 flex-1 break-words">{shownNotice.text}</span>
          {notice && <Button variant="ghost" size="icon-sm" label="Dismiss" onClick={() => setNotice(null)}><X /></Button>}
        </div>
      )}
    </div>
    {logsOpen && page && <LogStrip runID={runID} logs={logs} onClose={() => setLogsOpen(false)} />}
    <AlertDialog open={confirmation !== null} onOpenChange={(open) => { if (!open && !inFlight.current) setConfirmation(null) }}>
      <AlertDialogContent onCloseAutoFocus={(event) => { event.preventDefault(); menuTrigger.current?.focus() }}>
        <AlertDialogHeader>
          <AlertDialogTitle>{confirmation?.action === 'close' ? 'Close shared page?' : 'Reset shared browser?'}</AlertDialogTitle>
          <AlertDialogDescription>{confirmation?.action === 'close'
            ? 'Close this shared page for everyone? Other pages and the browser remain.'
            : 'All pages, cookies and logins will be lost.'}</AlertDialogDescription>
        </AlertDialogHeader>
        {!confirmationCurrent && <p role="alert" className="text-ui text-state-failed">The browser page or control changed. Cancel and choose the action again.</p>}
        {notice?.tone === 'failed' && <p role="alert" className="break-words text-ui text-state-failed">{notice.text}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending !== null}>Cancel</AlertDialogCancel>
          <AlertDialogAction disabled={pending !== null || !confirmationCurrent} onClick={(event) => { event.preventDefault(); confirm() }}>{confirmation?.action === 'close' ? 'Close page' : 'Reset browser'}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
}
