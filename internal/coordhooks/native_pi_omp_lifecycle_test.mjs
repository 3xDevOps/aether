// Node 22.13+; run both native adapter suites with make test-native-hooks.
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { stripTypeScriptTypes } from 'node:module'
import { EventEmitter } from 'node:events'
import { test } from 'node:test'
import vm from 'node:vm'
import { fileURLToPath } from 'node:url'

const flush = () => new Promise(resolve => setImmediate(resolve))
const pointer = 'Aether has pending mail. Read aether-internal inbox and explicitly acknowledge it.'

async function fixture(harness, inheritedOwner, settings = {}) {
  const handlers = new Map()
  const calls = []
  const sent = []
  const errors = []
  const timers = new Map()
  const beforeModel = new Set()
  const beforeQueue = new Set()
  const terminalInput = new Set()
  let idle = true
  let timerID = 0
  let active = 0
  let maximumActive = 0
  let sessionID = 'root'
  let sessionFile = '/root.jsonl'
  let signal
  const manager = { getSessionId: () => sessionID, getSessionFile: () => sessionFile }
  const ctx = {
    sessionManager: manager, hasUI: true, mode: 'tui',
    ui: {
      notify: message => errors.push(message),
      onTerminalInput: handler => { terminalInput.add(handler); return () => terminalInput.delete(handler) },
    },
    get signal() { return signal },
    isIdle: () => idle,
  }
  const agent = {
    isAborting: false,
    addBeforeModelCallHook: handler => { beforeModel.add(handler); return () => beforeModel.delete(handler) },
    addBeforeQueuedMessageDequeueHook: handler => { beforeQueue.add(handler); return () => beforeQueue.delete(handler) },
  }
  let main = { session: { sessionManager: manager, agent } }
  const api = {
    on: (name, handler) => {
      if (!handlers.has(name)) handlers.set(name, [])
      handlers.get(name).push(handler)
    },
    sendMessage: (message, options) => sent.push({ message, options }),
    pi: { MAIN_AGENT_ID: 'Main', AgentRegistry: { global: () => ({ get: () => main }) } },
  }
  function execFile(_file, args, _options, callback) {
    active++
    maximumActive = Math.max(maximumActive, active)
    const stdin = new EventEmitter()
    const call = { action: args[2], killed: false, done: false }
    stdin.end = input => { call.input = JSON.parse(input) }
    call.finish = (reply, code) => {
      if (call.done) return
      call.done = true
      active--
      callback(code ? Object.assign(new Error(`helper exit ${code}`), { code }) : null,
        typeof reply === 'string' ? reply : JSON.stringify(reply), code ? `helper exit ${code}` : '')
    }
    call.kill = () => {
      call.killed = true
      // Real child exit is asynchronous; a new helper must wait for this callback.
      queueMicrotask(() => call.finish('', 1))
      return true
    }
    call.stdin = stdin
    calls.push(call)
    return call
  }
  const sandbox = vm.createContext({
    AbortController, Set, Map, Promise, Date, JSON, Error, console: { error: message => errors.push(message) },
    process: {
      pid: 77, cwd: () => '/test-project',
      env: { AETHER_NATIVE_HOOK_OWNER: inheritedOwner, AETHER_MANAGED_NATIVE_WAKE: settings.managedPath },
    },
    setTimeout: (fn, ms) => { const id = ++timerID; timers.set(id, { fn, ms }); return id },
    clearTimeout: id => timers.delete(id),
  })
  const childModule = new vm.SyntheticModule(['execFile'], function () { this.setExport('execFile', execFile) }, { context: sandbox })
  const nativeModule = new vm.SyntheticModule(['SettingsManager', 'getAgentDir'], function () {
    this.setExport('SettingsManager', { create: () => ({
      getGlobalSettings: () => settings.global ?? {},
      getProjectSettings: () => settings.project ?? {},
      drainErrors: () => settings.errors ?? [],
    }) })
    this.setExport('getAgentDir', () => '/unused')
  }, { context: sandbox })
  const urlModule = new vm.SyntheticModule(['fileURLToPath'], function () { this.setExport('fileURLToPath', fileURLToPath) }, { context: sandbox })
  const tuiModule = new vm.SyntheticModule(['getKeybindings'], function () {
    this.setExport('getKeybindings', () => ({
      matches: (data, action) => action === 'app.interrupt' && data === 'configured-interrupt',
    }))
  }, { context: sandbox })
  const source = stripTypeScriptTypes(await readFile(new URL(`./${harness}.ts`, import.meta.url), 'utf8'))
  const module = new vm.SourceTextModule(source, {
    context: sandbox,
    initializeImportMeta: meta => { meta.url = settings.moduleURL ?? `file:///manual/${harness}.ts` },
  })
  await module.link(specifier => specifier === 'node:child_process' ? childModule :
    specifier === 'node:url' ? urlModule : specifier === '@earendil-works/pi-tui' ? tuiModule : nativeModule)
  await module.evaluate()
  function load(target = api) { module.namespace.default(target) }
  async function emit(name, event = {}, other = ctx) {
    const results = []
    for (const handler of handlers.get(name) ?? []) results.push(await handler({ type: name, ...event }, other))
    await flush()
    return results
  }
  async function begin() { await emit('session_start', { reason: 'startup' }) }
  function pending() { return calls.filter(call => !call.done).at(-1) }
  async function reply(ids, admitted = true) {
    const call = pending()
    assert.ok(call, 'the owning root has a pending helper')
    call.finish({ wait_supported: true, unread_message_ids: ids, wake_admitted: admitted, context: admitted ? pointer : '' })
    await flush()
  }
  async function humanStart(source = 'interactive') {
    idle = false
    await emit('input', { text: 'continue', source })
    await emit('before_agent_start', { prompt: 'continue' })
    signal = new AbortController().signal
    await emit('agent_start')
    await emit('message_start', { message: { role: 'user', content: 'continue' } })
    for (const hook of beforeModel) hook(signal)
    await flush()
  }
  async function abort() {
    const controller = new AbortController()
    signal = controller.signal
    idle = false
    await emit('agent_start')
    for (const hook of beforeModel) hook(signal)
    controller.abort()
    await flush()
  }
  async function finishTurn(stopReason = 'stop', history = []) {
    const message = { role: 'assistant', stopReason, content: [] }
    await emit('message_end', { message })
    await emit('turn_end', { message, toolResults: [] })
    await emit('agent_end', { messages: [...history, message] })
    // Core finishRun clears its signal before session retry/maintenance/settlement.
    signal = undefined
    await emit('agent_before_settle', { outcome: stopReason === 'error' ? 'error' : stopReason === 'aborted' ? 'aborted' : 'completed' })
    idle = true
    await emit('agent_settled')
  }
  load()
  return {
    api, ctx, calls, sent, errors, timers, handlers, begin, emit, reply, pending, load, humanStart, abort, finishTurn,
    setIdle: value => { idle = value },
    clearSignal: () => { signal = undefined },
    interrupt: () => { for (const handler of terminalInput) assert.equal(handler('configured-interrupt'), undefined) },
    setSession: (id, file) => { sessionID = id; sessionFile = file },
    replaceMain: () => { main = { session: { sessionManager: manager, agent } } },
    get maximumActive() { return maximumActive },
    async close() { await emit('session_shutdown', { reason: 'quit' }) },
  }
}

for (const harness of ['omp', 'pi']) {
  test(`${harness}: held observations remain eligible; admitted IDs deduplicate and same-count replacements wake`, async () => {
    const f = await fixture(harness)
    await f.begin()
    await f.reply(['a'], false)
    assert.equal(f.sent.length, 0)
    assert.deepEqual(f.pending().input.seen_message_ids, ['a'])
    await f.reply(['a'])
    assert.equal(f.sent.length, 1)
    await f.emit('message_start', { message: { role: 'custom', ...f.sent[0].message } })
    await f.finishTurn()
    await f.reply(['a'])
    assert.equal(f.sent.length, 1, 'unchanged admitted mail never starts another turn')
    await f.reply(['b'])
    assert.equal(f.sent.length, 2, 'same unread count does not hide replacement IDs')
    await f.close()
  })

  test(`${harness}: one outstanding native hint coalesces a busy burst without a second queue`, async () => {
    const f = await fixture(harness)
    await f.begin()
    await f.reply(['a'])
    if (harness === 'omp') {
      await f.reply(['a', 'b'])
      await f.reply(['a', 'b', 'c'])
    } else {
      assert.equal(f.pending(), undefined, 'busy pi leaves mail durable until settlement')
    }
    assert.equal(f.sent.length, 1)
    await f.emit('message_start', { message: { role: 'custom', ...f.sent[0].message } })
    await f.finishTurn()
    await f.reply(['a', 'b', 'c'])
    assert.equal(f.sent.length, 2)
    if (harness === 'omp') await f.reply(['a', 'b', 'c'])
    else assert.equal(f.pending(), undefined)
    assert.equal(f.sent.length, 2)
    await f.close()
  })

  test(`${harness}: acknowledgement removes notified IDs without turning a read-only observation into delivery`, async () => {
    const f = await fixture(harness)
    await f.begin()
    await f.reply(['a'])
    await f.emit('message_start', { message: { role: 'custom', ...f.sent[0].message } })
    await f.finishTurn()
    await f.reply([])
    assert.deepEqual(f.pending().input.seen_message_ids, [])
    await f.reply(['b'], false)
    assert.equal(f.sent.length, 1)
    await f.reply(['b'])
    assert.equal(f.sent.length, 2)
    await f.close()
  })

  test(`${harness}: Stop cancels the wait; input or extension continuations alone cannot resume it`, async () => {
    const f = await fixture(harness)
    await f.begin()
    const waiting = f.pending()
    await f.abort()
    assert.equal(waiting.killed, true)
    assert.equal(f.pending(), undefined)
    await f.emit('input', { text: 'not yet admitted', source: 'interactive' })
    assert.equal(f.pending(), undefined)
    await f.humanStart('extension')
    assert.equal(f.pending(), undefined)
    await f.humanStart()
    await f.finishTurn()
    await f.reply(['after-stop'])
    assert.equal(f.sent.length, 1)
    await f.close()
  })

  test(`${harness}: duplicate manual/managed loads supersede old handlers and preserve deduplication`, async () => {
    const f = await fixture(harness)
    await f.begin()
    await f.reply(['a'])
    await f.emit('message_start', { message: { role: 'custom', ...f.sent[0].message } })
    await f.finishTurn()
    const old = f.pending()
    f.load()
    await f.emit('session_start', { reason: 'reload' })
    assert.equal(old.killed, true)
    await f.reply(['a'])
    assert.equal(f.sent.length, 1)
    await f.reply(['b'])
    assert.equal(f.sent.length, 2)
    assert.equal(f.maximumActive, 1)
    await f.close()
  })

  test(`${harness}: context preempts the wait and shutdown rejects late context writes`, async () => {
    const f = await fixture(harness)
    await f.begin()
    const old = f.pending()
    const contextResult = f.emit('context', { messages: [{ role: 'user', content: 'hello' }] })
    await flush()
    assert.equal(old.killed, true)
    assert.equal(f.pending().action, 'context')
    await f.close()
    assert.deepEqual(await contextResult, [undefined])
    assert.equal(f.maximumActive, 1)
    assert.equal(f.sent.length, 0)
  })

  test(`${harness}: session identity changes and sibling managers cannot receive stale wakes`, async () => {
    const f = await fixture(harness)
    await f.begin()
    await f.emit('session_start', { reason: 'startup' }, { ...f.ctx, sessionManager: { getSessionId: () => 'sibling', getSessionFile: () => '/sibling' } })
    f.setSession('replacement', '/replacement')
    await f.reply(['stale'])
    assert.equal(f.sent.length, 0)
    assert.equal(f.pending(), undefined)
  })

  test(`${harness}: approval waits suppress dispatch until resolved`, async () => {
    const f = await fixture(harness)
    await f.begin()
    const before = f.pending()
    await f.emit(harness === 'omp' ? 'tool_approval_requested' : 'ui_prompt_start', { toolCallId: 'call' })
    assert.equal(before.killed, true)
    assert.equal(f.pending(), undefined)
    await f.emit(harness === 'omp' ? 'tool_approval_resolved' : 'ui_prompt_end', { toolCallId: 'call', approved: true })
    await f.reply(['approved'])
    assert.equal(f.sent.length, 1)
    await f.close()
  })

  test(`${harness}: unsupported helpers stop while transient failures have a bounded retry budget`, async () => {
    const unsupported = await fixture(harness)
    await unsupported.begin()
    unsupported.pending().finish('', 2)
    await flush()
    assert.equal(unsupported.pending(), undefined)
    assert.equal(unsupported.timers.size, 0)
    assert.match(unsupported.errors[0], /helper exit 2/)
    await unsupported.close()

    const recoverable = await fixture(harness)
    await recoverable.begin()
    for (let attempt = 0; attempt < 6; attempt++) {
      recoverable.pending().finish('', 1)
      await flush()
      if (attempt < 5) {
        assert.equal(recoverable.timers.size, 1)
        const [id, timer] = recoverable.timers.entries().next().value
        recoverable.timers.delete(id)
        timer.fn()
        await flush()
      }
    }
    assert.equal(recoverable.pending(), undefined)
    assert.equal(recoverable.timers.size, 0)
    assert.equal(recoverable.errors.length, 1, 'repeated identical transport failures are not a notification storm')
    await recoverable.close()
  })

  test(`${harness}: descendant processes never claim the root mailbox`, async () => {
    const f = await fixture(harness, '76')
    await f.begin()
    assert.equal(f.calls.length, 0)
    assert.equal(f.handlers.size, 0)
  })
}

test('pi: explicit replacement owns its new manager, but an unrelated session does not', async () => {
  const f = await fixture('pi')
  await f.begin()
  await f.emit('session_shutdown', { reason: 'resume', targetSessionFile: '/next.jsonl' })
  const next = { ...f.ctx, sessionManager: { getSessionId: () => 'next', getSessionFile: () => '/next.jsonl' } }
  await f.emit('session_start', { reason: 'resume', previousSessionFile: '/wrong.jsonl' }, next)
  assert.equal(f.pending(), undefined)
  await f.emit('session_start', { reason: 'resume', previousSessionFile: '/root.jsonl' }, next)
  await f.reply(['next-mail'])
  assert.equal(f.sent.length, 1)
  await f.emit('session_shutdown', { reason: 'quit' }, next)
})

test('omp: replacing Main during a helper wait cannot dispatch into its successor', async () => {
  const f = await fixture('omp')
  await f.begin()
  f.replaceMain()
  await f.reply(['stale'])
  assert.equal(f.sent.length, 0)
  assert.equal(f.pending(), undefined)
})

test('pi: reload preserves intentional Stop, while an explicit new in-memory session resumes ownership', async () => {
  const f = await fixture('pi')
  f.setSession('root', undefined)
  await f.begin()
  await f.abort()
  await f.emit('session_shutdown', { reason: 'reload' })
  f.load()
  await f.emit('session_start', { reason: 'reload' })
  assert.equal(f.pending(), undefined)
  await f.emit('session_before_switch', { reason: 'new' })
  await f.emit('session_shutdown', { reason: 'new', targetSessionFile: undefined })
  // Native pi replaces the Agent runtime as well as its SessionManager.
  // Its new context cannot retain the old Agent's aborted signal.
  const next = {
    ...f.ctx, signal: undefined, isIdle: () => true,
    sessionManager: { getSessionId: () => 'new-memory', getSessionFile: () => undefined },
  }
  f.load()
  await f.emit('session_start', { reason: 'new', previousSessionFile: undefined }, next)
  await f.reply(['new-session'])
  assert.equal(f.sent.length, 1)
  await f.emit('session_shutdown', { reason: 'quit' }, next)
})

test('omp: a before-switch gap cannot admit a wake into the outgoing session', async () => {
  const f = await fixture('omp')
  await f.begin()
  const outgoing = f.pending()
  await f.emit('session_before_switch', { reason: 'new' })
  assert.equal(outgoing.killed, true)
  assert.equal(f.pending(), undefined)
  f.setSession('new', '/new.jsonl')
  await f.emit('session_switch', { reason: 'new', previousSessionFile: '/root.jsonl' })
  await f.reply(['new-session'])
  assert.equal(f.sent.length, 1)
  await f.close()
})

test('pi: managed native exclusions fail closed without disabling an explicit manual copy', async () => {
  const settings = {
    managedPath: '/run/aether/aether.ts',
    moduleURL: 'file:///run/aether/aether.ts',
    project: { packages: [{ source: 'local-extension', extensions: ['!aether.ts'] }] },
  }
  const managed = await fixture('pi', undefined, settings)
  await managed.begin()
  assert.equal(managed.pending(), undefined)
  assert.equal(managed.handlers.size, 0)
  assert.equal(managed.errors.length, 1)
  const manual = await fixture('pi', undefined, { ...settings, moduleURL: 'file:///manual/pi.ts' })
  await manual.begin()
  await manual.reply(['mail'])
  assert.equal(manual.sent.length, 1)
  await manual.close()
})

test('pi: unreadable managed native settings cannot silently enable the receiver', async () => {
  const f = await fixture('pi', undefined, {
    managedPath: '/run/aether/aether.ts', moduleURL: 'file:///run/aether/aether.ts',
    errors: [{ error: new Error('unreadable configuration') }],
  })
  await f.begin()
  assert.equal(f.pending(), undefined)
  assert.equal(f.errors.length, 1)
})

for (const harness of ['omp', 'pi']) {
  test(`${harness}: loading a sibling runtime cannot supersede the owning root`, async () => {
    const f = await fixture(harness)
    await f.begin()
    const original = f.pending()
    const siblingHandlers = new Map()
    f.load({ ...f.api, on: (name, handler) => siblingHandlers.set(name, handler) })
    const sibling = { ...f.ctx, sessionManager: { getSessionId: () => 'sibling', getSessionFile: () => '/sibling' } }
    await siblingHandlers.get('session_start')({ type: 'session_start', reason: 'startup' }, sibling)
    assert.equal(original.killed, false)
    await f.reply(['root-mail'])
    assert.equal(f.sent.length, 1)
    await f.close()
  })
}

test('omp: a historical aborted assistant cannot pause a later successful user run', async () => {
  const f = await fixture('omp')
  await f.begin()
  const stopped = { role: 'assistant', stopReason: 'aborted', content: [] }
  await f.emit('message_end', { message: stopped })
  await f.emit('agent_end', { messages: [stopped] })
  assert.equal(f.pending(), undefined)
  await f.humanStart()
  await f.finishTurn('stop', [stopped])
  await f.reply(['after-success'])
  assert.equal(f.sent.length, 1)
  await f.close()
})

test('pi: busy observations require successful native settlement and fresh admission', async () => {
  const f = await fixture('pi')
  await f.begin()
  await f.reply(['during-work'], false)
  const idleWait = f.pending()
  await f.humanStart()
  assert.equal(f.sent.length, 0, 'no Aether hint may remain in the native busy queue')
  assert.equal(f.pending(), undefined, 'busy context calls do not restart wake helpers')
  const message = { role: 'assistant', stopReason: 'stop', content: [] }
  await f.emit('message_end', { message })
  await f.emit('turn_end', { message, toolResults: [] })
  await f.emit('agent_end', { messages: [message] })
  f.clearSignal()
  assert.equal(f.sent.length, 0, 'core agent_end is before native cleanup')
  await f.emit('agent_before_settle', { outcome: 'completed' })
  f.setIdle(true)
  await f.emit('agent_settled')
  assert.equal(idleWait.killed, true, 'an admission predating active work is stale')
  assert.deepEqual(f.pending().input.seen_message_ids, ['during-work'])
  assert.equal(f.pending().input.wait_seconds, 0)
  await f.reply(['during-work'], false)
  assert.equal(f.sent.length, 0, 'post-settlement admission still controls dispatch')
  await f.reply(['during-work'])
  assert.equal(f.sent.length, 1)
  await f.close()
})

test('pi: interrupting retry with no core signal leaves busy mail unqueued and paused', async () => {
  const f = await fixture('pi')
  await f.begin()
  await f.reply(['during-work'], false)
  await f.humanStart()
  const failure = { role: 'assistant', stopReason: 'error', content: [] }
  await f.emit('message_end', { message: failure })
  await f.emit('turn_end', { message: failure, toolResults: [] })
  await f.emit('agent_end', { messages: [failure] })
  f.clearSignal() // Core finishRun precedes the session's retry countdown.
  assert.equal(f.pending(), undefined)
  assert.equal(f.sent.length, 0)
  f.interrupt() // Native configured interrupt key; the observer must not consume it.
  await flush()
  assert.equal(f.pending(), undefined)
  await f.emit('agent_before_settle', { outcome: 'error' })
  f.setIdle(true)
  await f.emit('agent_settled')
  assert.equal(f.pending(), undefined)
  await f.humanStart()
  await f.finishTurn()
  await f.reply(['during-work', 'during-retry'])
  assert.equal(f.sent.length, 1)
  await f.close()
})

test('pi: a successful automatic retry can rearm without treating an earlier error as Stop', async () => {
  const f = await fixture('pi')
  await f.begin()
  await f.humanStart()
  const failure = { role: 'assistant', stopReason: 'error', content: [] }
  await f.emit('message_end', { message: failure })
  await f.emit('agent_end', { messages: [failure] })
  f.clearSignal()
  assert.equal(f.pending(), undefined)
  assert.equal(f.sent.length, 0)
  await f.emit('agent_start') // Native automatic retry: no user input event.
  await f.finishTurn()
  await f.reply(['during-retry'])
  assert.equal(f.sent.length, 1)
  await f.close()
})

test('pi: compaction cancellation uses its own signal and cannot admit observed mail', async () => {
  const f = await fixture('pi')
  await f.begin()
  f.setIdle(false)
  const compaction = new AbortController()
  await f.emit('session_before_compact', { reason: 'manual', signal: compaction.signal })
  assert.equal(f.pending(), undefined)
  assert.equal(f.sent.length, 0)
  compaction.abort()
  await flush()
  f.setIdle(true)
  await f.emit('session_compact_failed', { reason: 'manual', aborted: true, willRetry: false })
  assert.equal(f.pending(), undefined)
  await f.humanStart()
  await f.finishTurn()
  await f.reply(['during-compaction'])
  assert.equal(f.sent.length, 1)
  await f.close()
})

test('pi: compaction failure before its hook still preserves the native aborted outcome', async () => {
  const f = await fixture('pi')
  await f.begin()
  // Cancellation can occur during setup, before session_before_compact supplies
  // its separate signal. The native failed event is the remaining stop boundary.
  await f.emit('session_compact_failed', { reason: 'manual', aborted: true, willRetry: false })
  assert.equal(f.pending(), undefined)
  await f.emit('agent_settled')
  assert.equal(f.pending(), undefined)
  await f.humanStart()
  await f.finishTurn()
  await f.reply(['after-compaction-stop'])
  assert.equal(f.sent.length, 1)
  await f.close()
})

test('pi: rapid model contexts avoid redundant wake requests and recover transient hook throttling', async () => {
  const f = await fixture('pi')
  await f.begin()
  await f.humanStart()
  const wakesBefore = f.calls.filter(call => call.action === 'wake').length
  for (let turn = 0; turn < 35; turn++) {
    if (turn === 15) {
      const compaction = new AbortController()
      await f.emit('session_before_compact', { reason: 'threshold', signal: compaction.signal })
      await f.emit('session_compact', { reason: 'threshold', willRetry: false })
      // Native threshold compaction continues this same run, without agent_start.
      await f.emit('turn_start', { turnIndex: turn })
      assert.equal(f.pending(), undefined, 'in-run compaction must not rearm a busy wake helper')
    }
    const result = f.emit('context', { messages: [{ role: 'user', content: 'keep working' }] })
    await flush()
    assert.equal(f.pending().action, 'context')
    f.pending().finish(pointer)
    const [context] = await result
    assert.equal(context.messages.at(-1).content, pointer)
    assert.equal(f.pending(), undefined, 'one native context call must not spawn another busy wake')
  }
  assert.equal(f.calls.filter(call => call.action === 'wake').length, wakesBefore)
  await f.finishTurn()
  // Hook-budget exhaustion is classified by the real helper as transient exit1.
  f.pending().finish('', 1)
  await flush()
  assert.equal(f.sent.length, 0)
  assert.equal(f.timers.size, 1)
  const [id, timer] = f.timers.entries().next().value
  f.timers.delete(id)
  timer.fn()
  await flush()
  await f.reply(['after-rate-limit'])
  assert.equal(f.sent.length, 1, 'a temporary hook budget failure must not disable later idle wake')
  assert.equal(f.maximumActive, 1)
  await f.close()
})

test('pi: successful manual compaction requires cleanup and a fresh idle admission', async () => {
  const f = await fixture('pi')
  await f.begin()
  f.setIdle(false)
  const compaction = new AbortController()
  await f.emit('session_before_compact', { reason: 'manual', signal: compaction.signal })
  await f.emit('session_compact', { reason: 'manual', willRetry: false })
  await f.reply(['manual-compaction-mail'])
  assert.equal(f.sent.length, 0, 'successful compaction event precedes native cleanup')
  f.setIdle(true)
  await f.reply(['manual-compaction-mail'])
  assert.equal(f.sent.length, 0, 'busy-era admission cannot dispatch after cleanup')
  assert.equal(f.pending().input.wait_seconds, 0)
  await f.reply(['manual-compaction-mail'], false)
  assert.equal(f.sent.length, 0, 'idle state cannot bypass fresh server admission')
  await f.reply(['manual-compaction-mail'])
  assert.equal(f.sent.length, 1)
  await f.close()
})
