// Native pi 0.87.1; self-contained for manual or managed extension loading.
import { execFile } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { getAgentDir, SettingsManager } from '@earendil-works/pi-coding-agent'
import type { ExtensionAPI, ExtensionContext, SessionShutdownEvent } from '@earendil-works/pi-coding-agent'
import { getKeybindings } from '@earendil-works/pi-tui'

interface MailboxScope {
  manager: ExtensionContext['sessionManager'] | null
  sessionID: string | undefined
  active: boolean
  paused: boolean
  replacement: (Pick<SessionShutdownEvent, 'reason' | 'targetSessionFile'> & {
    previousSessionFile: string | undefined
  }) | null
  observed: string[]
  notified: Set<string>
  queued: boolean
  tail: Promise<void>
  dispose?: () => void
}

declare global {
  var __aetherPiMailboxScope: MailboxScope | undefined
}

const CUSTOM_TYPE = 'aether-mailbox-pending'
const WAKE_TYPE = 'aether-mailbox-wake'

export default function (pi: ExtensionAPI) {
  const owner = process.env.AETHER_NATIVE_HOOK_OWNER
  if (owner && owner !== String(process.pid)) return
  // pi's explicit -e bypasses resource exclusions. Only the managed copy
  // conservatively opts out; an explicitly loaded manual copy keeps native policy.
  const managedPath = process.env.AETHER_MANAGED_NATIVE_WAKE
  if (managedPath && managedPath === fileURLToPath(import.meta.url)) {
    try {
      const settings = SettingsManager.create(process.cwd(), getAgentDir())
      const errors = settings.drainErrors()
      if (errors.length) throw errors[0].error
      for (const config of [settings.getGlobalSettings(), settings.getProjectSettings()]) {
        const selectors = [...(config.extensions ?? [])]
        for (const pkg of config.packages ?? []) {
          if (typeof pkg !== 'string') selectors.push(...(pkg.extensions ?? []))
        }
        if (selectors.some(selector => selector.startsWith('!') || selector.startsWith('-'))) {
          console.error('Aether mailbox: automatic pi integration disabled by extension exclusions; explicitly load a trusted manual copy if desired.')
          return
        }
      }
    } catch (error) {
      console.error('Aether mailbox: automatic pi integration disabled because native extension settings could not be read.', error)
      return
    }
  }
  process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)
  // Keep identity, Stop state and deduplication across duplicate loads/reloads.
  // Native pi has one root runtime; independent multi-root SDK hosts are not supported.
  const scope: MailboxScope = globalThis.__aetherPiMailboxScope ??= {
    manager: null, sessionID: undefined, active: false, paused: false,
    replacement: null, observed: [], notified: new Set<string>(), queued: false,
    tail: Promise.resolve(),
  }
  let claimed = false
  let disposed = false
  let generation = 0
  let context: ExtensionContext | undefined
  let receiver: AbortController | undefined
  let contextCalls = 0
  let halted = false
  let userInput = false
  let prompts = 0
  let ready = false
  let outcome: 'completed' | 'aborted' | 'error' = 'completed'
  let detachInput: (() => void) | undefined
  let detachSignal: (() => void) | undefined
  let lastError = ''
  const requests = new Set<AbortController>()

  function owns(ctx = context) {
    return claimed && !disposed && scope.active && ctx !== undefined && scope.manager === ctx.sessionManager &&
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

  // Serialize every helper, including context calls and superseded loads. SIGKILL
  // closes a canceled helper before its successor can claim the root's socket wait.
  function helper(action: 'context' | 'wake', input: object, signal: AbortSignal) {
    const result = scope.tail.then(() => {
      signal.throwIfAborted()
      return new Promise<string>((resolve, reject) => {
        const child = execFile('/usr/local/bin/aether-internal', ['hook', 'pi', action], {
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
    let wasEligible = false
    while (owns() && generation === epoch && !signal.aborted && !scope.paused && !prompts && ready) {
      try {
        const eligible = ready && context?.isIdle() === true
        const waitSeconds = eligible && !wasEligible ? 0 : 30
        wasEligible = eligible
        const raw = await helper('wake', { seen_message_ids: scope.observed, wait_seconds: waitSeconds }, signal)
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
        if (!eligible || !ready || !context?.isIdle() || !reply.wake_admitted || !reply.context || scope.queued ||
            !scope.observed.some(id => !scope.notified.has(id))) continue
        if (context?.signal?.aborted) { pause(); return }
        // Do not leave a custom hint queued through pi's retry/compaction Stop:
        // defer busy mail until successful settlement and fresh helper admission.
        const dispatchIDs = scope.observed
        const dispatchManager = scope.manager
        const dispatchSessionID = scope.sessionID
        scope.queued = true
        ready = false
        try {
          pi.sendMessage({ customType: WAKE_TYPE, content: reply.context, display: false }, {
            triggerTurn: true, deliverAs: 'followUp',
          })
        } catch (error) {
          // A synchronous rejection is not helper failure or accepted delivery.
          // Preserve any newer Stop/session transition made by native callbacks.
          if (owns() && generation === epoch && !signal.aborted) {
            scope.queued = false
            ready = true
          }
          report(error)
          return // Eligible native lifecycle activity may request fresh admission.
        }
        if (owns() && scope.manager === dispatchManager && scope.sessionID === dispatchSessionID) {
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
    if (!owns() || !ready || scope.paused || halted || prompts || contextCalls || receiver) return
    const controller = receiver = new AbortController()
    const epoch = generation
    // Both rejection paths are handled; a background error must not crash pi.
    void receive(controller, epoch).catch(report).finally(() => {
      if (receiver === controller) receiver = undefined
    })
  }

  function dispose() {
    disposed = true
    cancel()
    detachSignal?.()
    detachInput?.()
  }

  pi.on('session_start', (event, ctx) => {
    if (disposed) return
    const replacement = scope.replacement
    if (scope.manager && scope.manager !== ctx.sessionManager &&
        !(replacement && event.reason === replacement.reason &&
          event.previousSessionFile === replacement.previousSessionFile &&
          ctx.sessionManager.getSessionFile() === replacement.targetSessionFile)) return
    if (!claimed) {
      scope.dispose?.()
      scope.dispose = dispose
      claimed = true
    }
    cancel()
    if (scope.sessionID !== ctx.sessionManager.getSessionId()) {
      scope.observed = []
      scope.notified.clear()
      scope.queued = false
    }
    if (event.reason === 'new' || event.reason === 'resume' || event.reason === 'fork') scope.paused = false
    scope.manager = ctx.sessionManager
    scope.sessionID = ctx.sessionManager.getSessionId()
    scope.active = true
    scope.replacement = null
    context = ctx
    ready = ctx.isIdle()
    outcome = 'completed'
    watchSignal(ctx.signal)
    detachInput?.()
    if (ctx.mode === 'tui') detachInput = ctx.ui.onTerminalInput(data => {
      if (owns(ctx) && !ctx.isIdle() && !prompts && getKeybindings().matches(data, 'app.interrupt')) pause()
      // Observe native interruption without consuming keys or aborting an overlay.
      return undefined
    })
    start()
  })
  pi.on('session_before_switch', (_event, ctx) => { if (owns(ctx)) pause() })
  pi.on('session_before_fork', (_event, ctx) => { if (owns(ctx)) pause() })
  pi.on('session_shutdown', (event, ctx) => {
    if (!owns(ctx)) return
    scope.active = false
    cancel()
    detachSignal?.()
    detachInput?.()
    scope.replacement = ['new', 'resume', 'fork'].includes(event.reason) ? {
      reason: event.reason, previousSessionFile: ctx.sessionManager.getSessionFile(),
      targetSessionFile: event.targetSessionFile,
    } : null
  })
  pi.on('input', (event, ctx) => {
    if (owns(ctx)) userInput = event.source !== 'extension'
  })
  pi.on('agent_start', (_event, ctx) => {
    if (!owns(ctx)) return
    context = ctx
    ready = false
    // Busy pi cannot dispatch a wake. Retain the observed set and let native
    // per-model context calls run without restarting a second helper each time.
    cancel()
    outcome = 'completed'
    watchSignal(ctx.signal)
  })
  pi.on('message_end', (event, ctx) => {
    if (!owns(ctx) || event.message.role !== 'assistant') return
    outcome = event.message.stopReason === 'aborted' ? 'aborted' :
      event.message.stopReason === 'error' ? 'error' : 'completed'
    if (outcome === 'aborted') pause()
  })
  pi.on('agent_end', (_event, ctx) => {
    if (!owns(ctx)) return
    // Core agent_end precedes retry/compaction and is not native settlement.
    ready = false
  })
  pi.on('agent_before_settle', (event, ctx) => {
    if (owns(ctx)) outcome = event.outcome
  })
  pi.on('agent_settled', (_event, ctx) => {
    if (!owns(ctx)) return
    scope.queued = false
    if (outcome !== 'completed') { pause(); return }
    ready = true
    context = ctx
    // Reject any response admitted while busy; request fresh admission now.
    cancel()
    start()
  })
  pi.on('session_before_compact', (event, ctx) => {
    if (!owns(ctx)) return
    ready = false
    cancel()
    watchSignal(event.signal)
  })
  pi.on('session_compact', (event, ctx) => {
    if (!owns(ctx)) return
    watchSignal(ctx.signal)
    // Threshold compaction can return to the SAME core run, with only turn_start
    // next. Automatic compaction must wait for that run's agent_settled.
    if (event.reason !== 'manual') return
    ready = true
    // Manual compaction has no continuing core run, but cleanup follows this
    // event. Admission requires isIdle both before its request and at dispatch.
    cancel()
    start()
  })
  pi.on('session_compact_failed', (event, ctx) => {
    if (owns(ctx) && event.aborted) pause()
  })
  pi.on('message_start', (event, ctx) => {
    if (!owns(ctx)) return
    if (event.message.role === 'custom' && event.message.customType === WAKE_TYPE) scope.queued = false
    // Merely seeing input is not acceptance: another extension may handle it.
    if (event.message.role === 'user' && userInput) {
      userInput = false
      scope.paused = false
      context = ctx
      start()
    }
  })
  pi.on('ui_prompt_start', (_event, ctx) => {
    if (!owns(ctx)) return
    prompts++
    cancel()
  })
  pi.on('ui_prompt_end', (_event, ctx) => {
    if (!owns(ctx)) return
    prompts = Math.max(0, prompts - 1)
    start()
  })
  pi.on('context', async (event, ctx) => {
    if (!owns(ctx)) return
    context = ctx
    contextCalls++
    // A model boundary takes priority over the background long poll.
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
        role: 'custom', customType: CUSTOM_TYPE, content, display: false, timestamp: Date.now(),
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
