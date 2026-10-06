// Shared native TUI status extension: pi 0.87.1 and oh-my-pi 18.3.1.
// No vendor imports: the hosts publish their extension types under different names.
const REPORTER = '/opt/aether/aether-server'
const OWNER = 'AETHER_STATUS_OWNER'
const CHAIN = '__aetherStatusReports'
const IDLE_RECHECK_MS = 25
const IDLE_RECHECK_MAX_MS = 250

type InputRequest = { id: string; session_id: string; kind: string }
const shared: {
  posts: Promise<void>; warned: boolean; pending: Map<string, InputRequest>;
  busy: Set<string>; state?: string; session?: string; dispose?: () => void;
} = globalThis[CHAIN] || (globalThis[CHAIN] = {
  posts: Promise.resolve(), warned: false, pending: new Map(), busy: new Set(),
})
const key = (session: string, kind: string, id: string) => JSON.stringify([session, kind, id])

function warnOnce(err: unknown): void {
  if (shared.warned) return
  shared.warned = true
  console.warn('[aether] status report failed:', err)
}

function spawnReport(body: string): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>()
  try {
    const { spawn } = require('child_process')
    const child = spawn(REPORTER, ['report', 'pi', '--json', body], { stdio: ['ignore', 'ignore', 'pipe'] })
    child.stderr?.setEncoding('utf8')
    child.stderr?.on('data', (chunk: string) => {
      const line = chunk.split('\n')[0].trim()
      if (line) warnOnce(line)
    })
    child.on('error', (err: unknown) => { warnOnce(err); resolve() })
    child.on('close', () => resolve())
  } catch (err) { warnOnce(err); resolve() }
  return promise
}

function report(): void {
  const body = JSON.stringify({
    ...(shared.state ? { state: shared.state } : {}),
    ...(shared.state === 'waiting' ? { reason: 'agent idle' } : {}),
    ...(shared.session ? { session_id: shared.session } : {}),
    input_updates: [{ operation: 'replace', requests: [...shared.pending.values()] }],
  })
  // Identical Working reports are heartbeats even without terminal/file output.
  shared.posts = shared.posts.then(() => spawnReport(body)).catch(warnOnce)
}

export default function (pi): void {
  const self = String(process.pid)
  const owner = process.env[OWNER]
  if (owner && owner !== self) return
  process.env[OWNER] = self
  let disposed = false
  let claimed = false
  let session = `process:${self}`
  let ended = false
  let settledSupported = false
  let generation = 0
  let recheck: NodeJS.Timeout | undefined
  let detach: (() => void) | undefined
  let mainSession

  function cancel(): void {
    generation++
    clearTimeout(recheck)
    recheck = undefined
  }
  const dispose = () => { disposed = true; cancel(); detach?.() }
  const on = (event: string, handler) => pi.on(event, (value, ctx) => {
    if (disposed) return
    const main = pi.pi?.AgentRegistry?.global()?.get(pi.pi.MAIN_AGENT_ID)?.session
    if (main && main.sessionManager !== ctx?.sessionManager) return
    if (!claimed) {
      shared.dispose?.()
      shared.dispose = dispose
      claimed = true
    }
    handler(value, ctx)
  })
  const sessionID = (event, ctx): string => event?.sessionId || ctx?.sessionManager?.getSessionId() || session

  function clearSession(id: string): void {
    shared.busy.delete(id)
    for (const [identity, request] of shared.pending) if (request.session_id === id) shared.pending.delete(identity)
  }
  function update(operation: 'open' | 'close', kind: string, id: unknown, scope: string): void {
    if (typeof id !== 'string' || !id || !scope) return
    const identity = key(scope, kind, id)
    if (operation === 'open') shared.pending.set(identity, { id, session_id: scope, kind })
    else if (!shared.pending.delete(identity)) return
    report()
  }
  function execution(active: boolean, scope = session): void {
    if (active) shared.busy.add(scope)
    else shared.busy.delete(scope)
    shared.state = shared.busy.size ? 'working' : 'waiting'
    report()
  }
  function adopt(id: string): void {
    session = id
    shared.session = id.startsWith('process:') ? undefined : id
  }
  function start(_event, ctx): void {
    cancel()
    adopt(sessionID(undefined, ctx))
    ended = false
    execution(true)
  }
  function end(): void {
    if (ended || disposed) return
    ended = true
    cancel()
    execution(false)
  }
  function recheckIdle(ctx, delay: number, epoch: number): void {
    recheck = setTimeout(() => {
      recheck = undefined
      if (disposed || generation !== epoch || settledSupported || ended) return
      try {
        if (ctx.isIdle()) end()
        else recheckIdle(ctx, Math.min(delay ? delay * 2 : IDLE_RECHECK_MS, IDLE_RECHECK_MAX_MS), epoch)
      } catch (error) { warnOnce(error) }
    }, delay)
    recheck.unref?.()
  }
  function bind(_event, ctx): void {
    const next = sessionID(undefined, ctx)
    if (next !== session) {
      const changed = shared.busy.has(session) || [...shared.pending.values()].some(request => request.session_id === session)
      clearSession(session)
      adopt(next)
      ended = false
      cancel()
      if (changed) execution(false)
    }
    // OMP's public session event follows cleanup and waitForIdle includes
    // agent-owned background work. Its extension agent_end fires earlier.
    const main = pi.pi?.AgentRegistry?.global()?.get(pi.pi.MAIN_AGENT_ID)?.session
    if (!main || main.sessionManager !== ctx?.sessionManager || main === mainSession) return
    detach?.()
    mainSession = main
    detach = main.subscribe(event => {
      if (disposed || event.type !== 'agent_end' || event.isTerminal === false) return
      const epoch = generation
      void main.waitForIdle().then(() => {
        if (!disposed && generation === epoch && ctx.isIdle()) end()
      }).catch(warnOnce)
    })
  }

  on('session_start', bind)
  on('session_switch', bind)
  on('session_branch', bind)
  on('session_shutdown', () => {
    cancel()
    shared.pending.clear()
    shared.busy.clear()
    execution(false)
    ended = true
    detach?.()
  })
  on('before_agent_start', start)
  on('agent_start', start)
  const toolStart = (event, ctx) => {
    const scope = sessionID(event, ctx)
    if (event?.toolName === 'ask' || event?.toolName === 'AskUserQuestion') {
      update('open', 'question', event.toolCallId, scope)
    } else if (!ended) execution(true, scope)
  }
  on('tool_call', toolStart)
  on('tool_execution_start', toolStart)
  on('tool_execution_end', (event, ctx) => {
    update('close', 'question', event?.toolCallId, sessionID(event, ctx))
  })
  on('tool_approval_requested', (event, ctx) => update('open', 'permission', event?.toolCallId, sessionID(event, ctx)))
  on('tool_approval_resolved', (event, ctx) => update('close', 'permission', event?.toolCallId, sessionID(event, ctx)))
  // Only pi 0.87.1 emits this outermost blocking-UI pair. OMP 18.3.1 has
  // no equivalent native TUI event; do not infer it from arbitrary ctx.ui calls.
  on('ui_prompt_start', (_event, ctx) => update('open', 'extension_ui', `ui-prompt:${self}`, sessionID(undefined, ctx)))
  on('ui_prompt_end', (_event, ctx) => update('close', 'extension_ui', `ui-prompt:${self}`, sessionID(undefined, ctx)))
  on('message_end', () => { if (!ended) execution(true) })
  on('agent_settled', () => { settledSupported = true; end() })
  on('agent_end', (event, ctx) => {
    if (mainSession || settledSupported || event?.willContinue) return
    if (!ctx || typeof ctx.isIdle !== 'function') { end(); return }
    cancel()
    recheckIdle(ctx, 0, generation)
  })
}
