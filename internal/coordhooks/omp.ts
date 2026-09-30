// oh-my-pi 18.3.1; self-contained for manual or managed extension loading.
import { execFile } from 'node:child_process'
import type { ExtensionAPI, ExtensionContext } from '@oh-my-pi/pi-coding-agent'

interface MailboxScope {
  manager: ExtensionContext['sessionManager'] | null
  sessionID: string | undefined
  active: boolean
  paused: boolean
  observed: string[]
  notified: Set<string>
  queued: boolean
  tail: Promise<void>
  dispose?: () => void
}

declare global {
  var __aetherOmpMailboxScope: MailboxScope | undefined
}

const CUSTOM_TYPE = 'aether-mailbox-pending'
const WAKE_TYPE = 'aether-mailbox-wake'

export default function (omp: ExtensionAPI) {
  const owner = process.env.AETHER_NATIVE_HOOK_OWNER
  if (owner && owner !== String(process.pid)) return
  process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)
  const scope: MailboxScope = globalThis.__aetherOmpMailboxScope ??= {
    manager: null, sessionID: undefined, active: false, paused: false,
    observed: [], notified: new Set<string>(), queued: false, tail: Promise.resolve(),
  }
  let claimed = false
  let disposed = false
  let generation = 0
  let context: ExtensionContext | undefined
  let receiver: AbortController | undefined
  let contextCalls = 0
  let halted = false
  let userInput = false
  let ready = false
  let outcome: 'completed' | 'error' | 'aborted' = 'completed'
  let lastError = ''
  let detachSignal: (() => void) | undefined
  const detachAgent: (() => void)[] = []
  const approvals = new Set<string>()
  const requests = new Set<AbortController>()
  let main = omp.pi.AgentRegistry.global().get(omp.pi.MAIN_AGENT_ID)

  function currentMain() { return omp.pi.AgentRegistry.global().get(omp.pi.MAIN_AGENT_ID) }

  function owns(ctx = context) {
    return claimed && !disposed && scope.active && ctx !== undefined && main === currentMain() &&
      main?.session?.sessionManager === ctx.sessionManager && scope.manager === ctx.sessionManager &&
      scope.sessionID === ctx.sessionManager.getSessionId()
  }

  function report(error: unknown) {
    const message = `Aether mailbox: ${error instanceof Error ? error.message : String(error)}`
    if (message === lastError) return
    lastError = message
    try {
      if (context?.hasUI) context.ui.notify(message, 'warning')
      else console.error(message)
    } catch { console.error(message) }
  }

  // The shared tail also orders context calls and superseded manual/managed
  // loads, so a canceled process exits before another helper takes its place.
  function helper(action: 'context' | 'wake', input: object, signal: AbortSignal) {
    const result = scope.tail.then(() => {
      signal.throwIfAborted()
      return new Promise<string>((resolve, reject) => {
        const child = execFile('/usr/local/bin/aether-internal', ['hook', 'omp', action], {
          timeout: action === 'wake' ? 35000 : 3000, killSignal: 'SIGKILL', maxBuffer: 65536,
        }, (error, stdout, stderr) => {
          signal.removeEventListener('abort', cancel)
          if (signal.aborted) reject(signal.reason)
          else if (error) {
            if (stderr.trim()) error.message = stderr.trim()
            reject(error)
          } else resolve(stdout.trim())
        })
        const cancel = () => { child.kill('SIGKILL') }
        signal.addEventListener('abort', cancel, { once: true })
        child.stdin?.on('error', () => { child.kill('SIGKILL') })
        child.stdin?.end(JSON.stringify(input))
        if (signal.aborted) cancel()
      })
    })
    scope.tail = result.then(() => {}, () => {})
    return result
  }

  function cancel() {
    generation++
    receiver?.abort()
    receiver = undefined
    for (const request of requests) request.abort()
  }

  function pause() {
    if (!owns()) return
    scope.paused = true
    ready = false
    userInput = false
    scope.queued = false // Native Stop owns clearing its own queue.
    cancel()
  }

  function watchSignal(signal?: AbortSignal) {
    detachSignal?.()
    if (!signal) return
    signal.addEventListener('abort', pause, { once: true })
    detachSignal = () => signal.removeEventListener('abort', pause)
    if (signal.aborted) pause()
  }

  async function delay(ms: number, signal: AbortSignal) {
    if (signal.aborted) return
    await new Promise<void>(resolve => {
      const finish = () => { clearTimeout(timer); signal.removeEventListener('abort', finish); resolve() }
      const timer = setTimeout(finish, ms)
      signal.addEventListener('abort', finish, { once: true })
    })
  }

  async function receive(controller: AbortController, epoch: number) {
    const { signal } = controller
    let failures = 0
    let first = true
    while (owns() && generation === epoch && !signal.aborted && !scope.paused && !approvals.size && ready) {
      try {
        if (!context?.isIdle()) return
        const raw = await helper('wake', { seen_message_ids: scope.observed, wait_seconds: first ? 0 : 30 }, signal)
        first = false
        if (!owns() || generation !== epoch || signal.aborted) return
        const reply = JSON.parse(raw)
        if (reply.wait_supported !== true) {
          halted = true
          report('native wake is unsupported; use the inbox or upgrade the helper/server')
          return
        }
        if (!Array.isArray(reply.unread_message_ids) || reply.unread_message_ids.length > 100 ||
            reply.unread_message_ids.some((id: unknown) => typeof id !== 'string' || !id) ||
            typeof reply.wake_admitted !== 'boolean' || typeof reply.context !== 'string') {
          throw new Error('invalid wake helper response')
        }
        scope.observed = [...new Set<string>(reply.unread_message_ids)]
        const current = new Set(scope.observed)
        for (const id of scope.notified) if (!current.has(id)) scope.notified.delete(id)
        failures = 0
        lastError = ''
        if (!ready || !context?.isIdle()) return
        if (!reply.wake_admitted || !reply.context || scope.queued ||
            !scope.observed.some(id => !scope.notified.has(id))) continue
        if (main?.session?.agent.isAborting) { pause(); return }
        const dispatchIDs = scope.observed
        const dispatchManager = scope.manager
        const dispatchSessionID = scope.sessionID
        const dispatchMain = main
        scope.queued = true
        ready = false
        // Never leave a follow-up behind work that can consume the mail itself.
        try {
          omp.sendMessage({ customType: WAKE_TYPE, content: reply.context, attribution: 'agent', display: false }, {
            triggerTurn: true, deliverAs: 'followUp',
          })
        } catch (error) {
          // Leave rejected IDs eligible, but do not blindly retry native input.
          // A later lifecycle boundary must obtain fresh helper admission.
          if (owns() && generation === epoch && !signal.aborted) {
            scope.queued = false
            ready = true
          }
          report(error)
          return
        }
        if (owns() && main === dispatchMain && scope.manager === dispatchManager && scope.sessionID === dispatchSessionID) {
          for (const id of dispatchIDs) scope.notified.add(id)
        }
      } catch (error) {
        if (!owns() || generation !== epoch || signal.aborted) return
        report(error)
        const failure = error as { code?: number | string, killed?: boolean }
        if ((failure.code !== 1 && !failure.killed) || ++failures > 5) {
          halted = true
          return
        }
        await delay(Math.min(1000 * 2 ** (failures - 1), 30000), signal)
      }
    }
  }

  function start() {
    if (!owns() || !ready || !context?.isIdle() || scope.paused || halted || approvals.size || contextCalls || receiver) return
    const controller = receiver = new AbortController()
    const epoch = generation
    void receive(controller, epoch).catch(report).finally(() => {
      if (receiver === controller) receiver = undefined
    })
  }

  function settled(ctx: ExtensionContext) {
    if (!owns(ctx) || scope.paused) return
    ready = false
    cancel()
    if (outcome !== 'completed') { pause(); return }
    const epoch = generation
    const session = main?.session
    if (!session) return
    // Never await the session's drain from a callback that drain itself awaits.
    void session.waitForIdle().then(() => {
      if (!owns(ctx) || generation !== epoch || scope.paused || !ctx.isIdle()) return
      scope.queued = false
      ready = true
      context = ctx
      start()
    }).catch(error => { if (owns(ctx) && generation === epoch) report(error) })
  }

  function bind(ctx: ExtensionContext, replacement = false) {
    if (disposed || currentMain()?.session?.sessionManager !== ctx.sessionManager) return
    if (!claimed) {
      scope.dispose?.()
      scope.dispose = dispose
      claimed = true
    }
    cancel()
    detachSignal?.()
    for (const detach of detachAgent.splice(0)) detach()
    main = currentMain()
    context = ctx
    if (scope.sessionID !== ctx.sessionManager.getSessionId()) {
      scope.observed = []
      scope.notified.clear()
      scope.queued = false
      scope.paused = false
    }
    if (replacement) scope.paused = false
    scope.manager = ctx.sessionManager
    scope.sessionID = ctx.sessionManager.getSessionId()
    scope.active = true
    approvals.clear()
    ready = ctx.isIdle() && !scope.queued
    // OMP's ExtensionContext has no signal. These removable public Main-agent
    // hooks expose the exact run signal, including tools and queue continuations.
    const agent = main?.session?.agent
    if (agent) {
      detachAgent.push(agent.addBeforeModelCallHook(watchSignal))
      detachAgent.push(agent.addBeforeQueuedMessageDequeueHook(watchSignal))
    }
    if (main?.session) {
      detachAgent.push(main.session.subscribe(event => {
        if (!owns(ctx) || event.type !== 'agent_end') return
        // The public event is deferred through prompt cleanup; extension
        // agent_end is earlier and includes native retries/todo continuations.
        if (event.isTerminal === false) { ready = false; cancel(); return }
        settled(ctx)
      }))
    }
    start()
  }

  function dispose() {
    disposed = true
    cancel()
    detachSignal?.()
    for (const detach of detachAgent.splice(0)) detach()
  }
  omp.on('session_start', (_event, ctx) => bind(ctx))
  omp.on('session_before_switch', (_event, ctx) => { if (owns(ctx)) pause() })
  omp.on('session_before_branch', (_event, ctx) => { if (owns(ctx)) pause() })
  omp.on('session_switch', (_event, ctx) => bind(ctx, true))
  omp.on('session_branch', (_event, ctx) => bind(ctx, true))
  omp.on('session_shutdown', (_event, ctx) => {
    if (!owns(ctx)) return
    scope.active = false
    cancel()
    detachSignal?.()
    for (const detach of detachAgent.splice(0)) detach()
  })
  omp.on('input', (event, ctx) => {
    if (owns(ctx)) userInput = event.source !== 'extension'
  })
  omp.on('agent_start', (_event, ctx) => {
    if (!owns(ctx)) return
    context = ctx
    ready = false
    outcome = 'completed'
    cancel()
  })
  omp.on('message_end', (event, ctx) => {
    if (!owns(ctx) || event.message.role !== 'assistant') return
    // Retained agent_end history can contain an earlier, unrelated abort.
    outcome = event.message.stopReason === 'aborted' ? 'aborted' :
      event.message.stopReason === 'error' ? 'error' : 'completed'
    if (outcome === 'aborted') pause()
  })
  omp.on('message_start', (event, ctx) => {
    if (!owns(ctx)) return
    if (event.message.role === 'custom' && event.message.customType === WAKE_TYPE) scope.queued = false
    // Input hooks may be handled/rejected. Resume only once native processing
    // actually accepts a user message, never on a custom wake/extension turn.
    if (event.message.role === 'user' && userInput) {
      userInput = false
      scope.paused = false
      context = ctx
      start()
    }
  })
  omp.on('tool_approval_requested', (event, ctx) => {
    if (!owns(ctx)) return
    approvals.add(event.toolCallId)
    cancel()
  })
  omp.on('tool_approval_resolved', (event, ctx) => {
    if (!owns(ctx)) return
    approvals.delete(event.toolCallId)
    start()
  })
  omp.on('context', async (event, ctx) => {
    if (!owns(ctx)) return
    context = ctx
    contextCalls++
    receiver?.abort()
    receiver = undefined
    const epoch = generation
    const request = new AbortController()
    requests.add(request)
    try {
      const content = await helper('context', {}, request.signal)
      if (!owns(ctx) || generation !== epoch || request.signal.aborted) return
      const messages = event.messages.filter(message => message.role !== 'custom' ||
        (message.customType !== CUSTOM_TYPE && message.customType !== WAKE_TYPE))
      if (content) messages.push({
        role: 'custom', customType: CUSTOM_TYPE, content, attribution: 'agent', display: false, timestamp: Date.now(),
      })
      return { messages }
    } catch (error) {
      if (!request.signal.aborted && owns(ctx)) report(error)
    } finally {
      requests.delete(request)
      contextCalls--
      start()
    }
  })
}
