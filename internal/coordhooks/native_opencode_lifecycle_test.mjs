import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFile } from 'node:fs/promises'
import { randomUUID } from 'node:crypto'
import test from 'node:test'
import vm from 'node:vm'

const flush = () => new Promise(resolve => setImmediate(resolve))
const deferred = () => Promise.withResolvers()

async function harness(version) {
  const process = new EventEmitter()
  process.pid = 123
  process.env = {}
  const calls = []
  const prompts = []
  const errors = []
  const sleeps = []
  const sessions = new Map([
    ['root', { id: 'root', time: {}, location: { directory: '/workspace' }, outcome: 'succeeded' }],
    ['sibling', { id: 'sibling', time: {}, location: { directory: '/workspace' }, outcome: 'succeeded' }],
    ['child', { id: 'child', parentID: 'root', time: {}, location: { directory: '/workspace' }, outcome: 'succeeded' }],
  ])
  const statuses = {}
  const hooks = new Map()
  let adapter
  let cleanup
  let waitGate
  let getGate
  let promptGate
  let eventWaiter
  const events = []
  const sandbox = vm.createContext({
    process, AbortController, DOMException, console: { error: (...args) => errors.push(args) },
  })
  function execFile(_file, args, options, callback) {
    const stdin = new EventEmitter()
    const request = {
      command: args[2], done: false, killed: false,
      reply(value) {
        request.done = true
        callback(null, typeof value === 'string' ? value : JSON.stringify(value), '')
      },
      fail(code) {
        request.done = true
        callback(Object.assign(new Error('helper failed'), { code }), '', 'helper failed')
      },
    }
    stdin.end = data => {
      request.input = JSON.parse(data)
      calls.push(request)
    }
    options.signal.addEventListener('abort', () => {
      request.done = true
      request.killed = true
      callback(Object.assign(new Error('cancelled'), { name: 'AbortError' }), '', '')
    }, { once: true })
    return { stdin }
  }
  const modules = {
    'node:child_process': { execFile },
    'node:crypto': { randomUUID },
    'node:timers/promises': { setTimeout: (ms, _value, { signal }) => {
      const waiter = deferred()
      sleeps.push({ ms, resolve: waiter.resolve })
      signal.addEventListener('abort', () => waiter.reject(new DOMException('cancelled', 'AbortError')), { once: true })
      return waiter.promise
    } },
  }
  async function load(selectedVersion = version) {
    const source = await readFile(new URL(`opencode-v${selectedVersion}.js`, import.meta.url), 'utf8')
    const module = new vm.SourceTextModule(source, { context: sandbox })
    await module.link(specifier => {
      const exports = modules[specifier]
      if (!exports) throw new Error(`Standalone plugin cannot resolve ${specifier}`)
      return new vm.SyntheticModule(Object.keys(exports), function () {
        for (const [name, value] of Object.entries(exports)) this.setExport(name, value)
      }, { context: sandbox })
    })
    await module.evaluate()
    return module.namespace.default
  }
  async function emit(type, data = {}) {
    if (version === 1) await adapter.event({ event: { type, properties: data } })
    else {
      events.push({ type, data, location: { directory: '/workspace' } })
      eventWaiter?.resolve()
      await flush()
    }
  }
  const session = {
    async get(input) {
      const id = version === 1 ? input.path.id : input.sessionID
      if (getGate) await getGate.promise
      const info = sessions.get(id)
      if (!info) throw new Error('session deleted')
      return version === 1 ? { data: info } : info
    },
    async status() { return { data: statuses } },
    async wait() { if (waitGate) await waitGate.promise },
    async hook(name, fn) { hooks.set(name, fn) },
    async prompt(input) {
      const gate = promptGate
      promptGate = undefined
      if (gate?.before) await gate.promise
      await hooks.get('prompt')({ sessionID: input.sessionID, messageID: `msg_${randomUUID()}`, metadata: input.metadata })
      prompts.push(input)
      statuses[input.sessionID] = { type: 'busy' }
      await emit('session.execution.started', { sessionID: input.sessionID })
      if (gate && !gate.before) await gate.promise
    },
    async promptAsync(input) {
      const gate = promptGate
      promptGate = undefined
      if (gate?.before) await gate.promise
      await adapter['chat.message']({ sessionID: input.path.id, messageID: `msg_${randomUUID()}` }, { parts: input.body.parts })
      prompts.push(input)
      statuses[input.path.id] = { type: 'busy' }
      await emit('session.status', { sessionID: input.path.id, status: { type: 'busy' } })
      if (gate && !gate.before) await gate.promise
    },
  }
  const ctx = {
    session,
    location: { directory: '/workspace' },
    event: { async *subscribe({ signal }) {
      while (!signal.aborted) {
        if (events.length) { yield events.shift(); continue }
        eventWaiter = deferred()
        const resume = eventWaiter.resolve
        signal.addEventListener('abort', resume, { once: true })
        await eventWaiter.promise
        signal.removeEventListener('abort', resume)
      }
    } },
  }
  async function setup(selectedVersion = version) {
    const plugin = await load(selectedVersion)
    if (selectedVersion === 1) {
      adapter = await plugin({ client: { session }, directory: '/workspace' })
      cleanup = () => adapter.dispose()
    } else cleanup = await plugin.setup(ctx)
  }
  await setup()
  return {
    version, calls, prompts, errors, sleeps, sessions, statuses, emit, setup,
    get cleanup() { return cleanup },
    blockGet() { getGate = deferred(); return getGate },
    blockSettlement() { waitGate = deferred(); return waitGate },
    blockPrompt(before = true) { promptGate = { ...deferred(), before }; return promptGate },
    pending(command = 'wake') { return calls.filter(call => call.command === command && !call.done) },
    async prompt(id = 'root') {
      const event = { sessionID: id, messageID: `msg_human_${randomUUID()}` }
      if (version === 1) await adapter['chat.message'](event)
      else await hooks.get('prompt')(event)
    },
    async success(id = 'root') {
      statuses[id] = { type: 'idle' }
      sessions.get(id).outcome = 'succeeded'
      if (version === 1) {
        await emit('message.updated', { info: { sessionID: id, role: 'assistant', finish: 'stop', time: { completed: 1 } } })
        // V1's processor returns continue after finalizing the assistant, so
        // the next loop iteration publishes busy before it discovers finish.
        await emit('session.status', { sessionID: id, status: { type: 'busy' } })
        await emit('session.idle', { sessionID: id })
      } else await emit('session.execution.succeeded', { sessionID: id })
      await flush()
    },
    async stop() {
      sessions.get('root').outcome = 'interrupted'
      if (version === 1) await emit('message.updated', { info: { sessionID: 'root', role: 'assistant', error: { name: 'MessageAbortedError' } } })
      else await emit('session.execution.interrupted', { sessionID: 'root', reason: 'user' })
      await flush()
    },
    async context() {
      const draft = version === 1
        ? { messages: [{ info: { role: 'user', id: 'msg_user', sessionID: 'root' }, parts: [] }] }
        : { sessionID: 'root', system: [] }
      const done = version === 1
        ? adapter['experimental.chat.messages.transform']({}, draft)
        : hooks.get('context')(draft)
      return { draft, done }
    },
  }
}

function reply(request, ids, admitted = true) {
  assert.ok(request, 'receiver should have an outstanding bounded helper request')
  request.reply({ wait_supported: true, unread_message_ids: ids, wake_admitted: admitted, context: admitted ? 'Read aether-internal inbox.' : '' })
}

for (const version of [1, 2]) {
  test(`V${version} defers busy mail, admits held IDs once, and wakes again for new mail`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    assert.equal(h.pending().length, 0, 'busy root must not admit mail before completion')
    await h.success()
    reply(h.pending()[0], ['mail-a'], false)
    await flush()
    assert.equal(h.prompts.length, 0)
    assert.deepEqual(h.pending()[0].input.seen_message_ids, ['mail-a'])
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1, 'released hold must wake previously observed, unnotified IDs')
    await h.success()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1, 'unchanged unread IDs must not produce repeated turns')
    reply(h.pending()[0], ['mail-a', 'mail-b'])
    await flush()
    assert.equal(h.prompts.length, 2)
    const prompt = h.prompts[1]
    assert.equal(version === 1 ? prompt.path.id : prompt.sessionID, 'root')
    assert.equal(version === 1 ? prompt.body.parts[0].text : prompt.text, 'Read aether-internal inbox.')
  })

  test(`V${version} rejected wake retains unread mail until a later human turn completes`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    reply(h.pending()[0], ['accepted-mail'])
    await flush()
    await h.success()
    const gate = h.blockPrompt()
    reply(h.pending()[0], ['accepted-mail', 'rejected-mail'])
    await flush()
    gate.reject(new Error('native prompt rejected'))
    await flush()
    assert.equal(h.prompts.length, 1)
    assert.equal(h.pending().length, 0, 'rejection must not blindly retry the native mutation')
    await h.success()
    assert.equal(h.pending().length, 0, 'idle alone must not undo the rejection pause')
    await h.prompt()
    await h.success()
    reply(h.pending()[0], ['accepted-mail', 'rejected-mail'])
    await flush()
    assert.equal(h.prompts.length, 2, 'the same rejected unread message must get a new accepted wake')
    await h.success()
    reply(h.pending()[0], ['accepted-mail', 'rejected-mail'])
    await flush()
    assert.equal(h.prompts.length, 2, 'accepted recovery must coalesce unchanged unread mail')
  })

  test(`V${version} rejected mixed batch preserves earlier accepted notification`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    reply(h.pending()[0], ['accepted-mail'])
    await flush()
    await h.success()
    const gate = h.blockPrompt()
    reply(h.pending()[0], ['accepted-mail', 'rejected-mail'])
    await flush()
    gate.reject(new Error('native prompt rejected'))
    await flush()
    await h.prompt()
    await h.success()
    reply(h.pending()[0], ['accepted-mail'])
    await flush()
    assert.equal(h.prompts.length, 1, 'rollback must not clear notifications accepted by an earlier request')
  })

  test(`V${version} reserves unread mail while native acceptance trails lifecycle completion`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    const gate = h.blockPrompt(false)
    reply(h.pending()[0], ['mail-a'])
    await flush()
    await h.success()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1, 'pending acceptance must suppress duplicate delivery')
    gate.resolve()
    await flush()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1, 'acceptance after busy changed the epoch must retain coalescing')
    reply(h.pending()[0], ['mail-a', 'mail-b'])
    await flush()
    assert.equal(h.prompts.length, 2, 'new mail must still wake after the pending request settles')
  })

  test(`V${version} Stop fences a pending prompt and preserves its unread mail`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    const gate = h.blockPrompt()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    await h.stop()
    gate.resolve()
    await flush()
    await h.success()
    assert.equal(h.prompts.length, 0, 'a cancelled epoch must reject late native prompt admission')
    assert.equal(h.pending().length, 0, 'late rejection must not clear the Stop pause')
    await h.prompt()
    await h.success()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1, 'cancelled prompt must not suppress recovery after human input')
  })

  test(`V${version} late rejection cannot erase a newer accepted notification`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    const gate = h.blockPrompt()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    await h.prompt()
    await h.success()
    reply(h.pending()[0], [])
    await flush()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1)
    gate.reject(new Error('older native prompt rejected'))
    await flush()
    await h.success()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1, 'rollback must only release reservations owned by the rejected request')
  })

  test(`V${version} cancels Stop and stale helper replies until explicit root input`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    const pending = h.pending()[0]
    await h.stop()
    assert.equal(pending.killed, true)
    reply(pending, ['mail-a'])
    await h.success()
    assert.equal(h.prompts.length, 0)
    assert.equal(h.pending().length, 0, 'success/idle alone must not undo manual Stop')
    await h.prompt()
    await h.success()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1)
  })

  test(`V${version} rejects native dispatch when Stop wins an awaited session read`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    const gate = h.blockGet()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    await h.stop()
    gate.resolve()
    await flush()
    assert.equal(h.prompts.length, 0)
    assert.equal(h.pending().length, 0)
  })

  test(`V${version} children cannot bind and siblings retire rather than replace the root`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt('child')
    await h.success('child')
    assert.equal(h.pending().length, 0)
    await h.prompt()
    await h.success()
    const pending = h.pending()[0]
    await h.prompt('child')
    assert.equal(pending.killed, false)
    await h.prompt('sibling')
    assert.equal(pending.killed, true)
    reply(pending, ['mail-a'])
    await h.success('sibling')
    await h.prompt()
    await h.success()
    assert.equal(h.prompts.length, 0)
    assert.equal(h.pending().length, 0)
  })

  test(`V${version} serializes first root binding despite concurrent session lookups`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    const gate = h.blockGet()
    const first = h.prompt('root')
    const second = h.prompt('child')
    gate.resolve()
    await Promise.all([first, second])
    await h.success()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1)
    assert.equal(version === 1 ? h.prompts[0].path.id : h.prompts[0].sessionID, 'root')
  })

  test(`V${version} deletion cancels helper and discards in-flight trusted context`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    const wake = h.pending()[0]
    const { draft, done } = await h.context()
    const context = h.pending('context')[0]
    await h.emit('session.deleted', version === 1 ? { info: { id: 'root' } } : { sessionID: 'root' })
    assert.equal(wake.killed, true)
    assert.equal(context.killed, true)
    context.reply('late context')
    reply(wake, ['mail-a'])
    await done
    await flush()
    assert.equal(h.prompts.length, 0)
    assert.equal(version === 1 ? draft.messages.length : draft.system.length, version === 1 ? 1 : 0)
  })

  test(`V${version} duplicate load supersedes receiver without losing dedup or permitting co-load`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    reply(h.pending()[0], ['mail-a'])
    await flush()
    await h.success()
    const old = h.pending()[0]
    await h.setup()
    await flush()
    assert.equal(old.killed, true)
    reply(old, ['mail-b'])
    reply(h.pending()[0], ['mail-a'])
    await flush()
    assert.equal(h.prompts.length, 1, 'supersession must retain already-notified IDs')
    await assert.rejects(h.setup(version === 1 ? 2 : 1), /do not co-load/)
  })

  test(`V${version} unsupported server stops without retry; transport retries are bounded`, async t => {
    const h = await harness(version)
    t.after(() => h.cleanup())
    await h.prompt()
    await h.success()
    h.pending()[0].fail(2)
    await flush()
    assert.equal(h.pending().length, 0)
    assert.equal(h.sleeps.length, 0)
    await h.prompt()
    await h.success()
    for (let attempt = 0; attempt < 6; attempt++) {
      h.pending()[0].fail(1)
      await flush()
      if (attempt < 5) {
        h.sleeps[attempt].resolve()
        await flush()
      }
    }
    assert.equal(h.pending().length, 0)
    assert.deepEqual(h.sleeps.map(sleep => sleep.ms), [1000, 2000, 4000, 8000, 16000])
    assert.ok(h.errors.length > 0)
  })
}

test('V1 unclassified shell/setup idle cannot undo intentional Stop', async t => {
  const h = await harness(1)
  t.after(() => h.cleanup())
  await h.prompt()
  await h.emit('session.status', { sessionID: 'root', status: { type: 'busy' } })
  await h.emit('session.idle', { sessionID: 'root' })
  assert.equal(h.pending().length, 0)
  await h.prompt()
  await h.success()
  assert.equal(h.pending().length, 1)
})

test('V1 a new assistant invalidates prior completion before an unclassified idle', async t => {
  const h = await harness(1)
  t.after(() => h.cleanup())
  await h.prompt()
  await h.emit('message.updated', { info: { sessionID: 'root', role: 'assistant', finish: 'stop', time: { completed: 1 } } })
  await h.emit('session.status', { sessionID: 'root', status: { type: 'busy' } })
  await h.emit('message.updated', { info: { sessionID: 'root', role: 'assistant', time: { created: 2 } } })
  await h.emit('session.idle', { sessionID: 'root' })
  assert.equal(h.pending().length, 0, 'an incomplete newer assistant must not reuse old success')
  await h.prompt()
  await h.success()
  reply(h.pending()[0], ['mail-a'])
  await flush()
  assert.equal(h.prompts.length, 1, 'complete -> redundant busy -> idle must still wake')
})

test('V2 waits for native cleanup settlement before helper admission and rechecks interruption', async t => {
  const h = await harness(2)
  t.after(() => h.cleanup())
  await h.prompt()
  const gate = h.blockSettlement()
  await h.success()
  assert.equal(h.pending().length, 0, 'successful event is not yet native idle settlement')
  h.sessions.get('root').outcome = 'interrupted'
  gate.resolve()
  await flush()
  assert.equal(h.pending().length, 0, 'interrupted terminal state must not restart the run')
  assert.equal(h.prompts.length, 0)
})
