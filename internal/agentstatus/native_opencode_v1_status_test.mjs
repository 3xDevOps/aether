import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const flush = () => new Promise(resolve => setImmediate(resolve))
const request = (id, session_id, kind) => ({ id, session_id, kind })

async function reporter({ password = '', username = 'opencode' } = {}) {
  const reports = []
  const live = { permission: [], question: [] }
  const warnings = []
  const children = []
  const calls = []
  const timers = new Set()
  const errors = {}
  const host = new EventEmitter()
  host.env = { OPENCODE_SERVER_PASSWORD: password, OPENCODE_SERVER_USERNAME: username }
  let now = 0
  let error
  let responseGate
  const sandbox = vm.createContext({
    URL, Buffer, AbortSignal, AbortController,
    process: host,
    setTimeout(callback, delay) {
      const timer = { callback, at: now + delay, unref() {} }
      timers.add(timer)
      return timer
    },
    clearTimeout(timer) { timers.delete(timer) },
    fetch: async (url, options) => {
      const kind = url.pathname.slice(1)
      calls.push({ kind, signal: options.signal })
      const failure = errors[kind] ?? error
      const snapshot = structuredClone(live[kind])
      if (responseGate) {
        await new Promise((resolve, reject) => {
          const abort = () => reject(options.signal.reason)
          options.signal.addEventListener('abort', abort, { once: true })
          responseGate.promise.then(resolve, reject).finally(() => options.signal.removeEventListener('abort', abort))
        })
      }
      if (failure) throw failure
      const authorization = `Basic ${Buffer.from(`${username}:${password}`).toString('base64')}`
      if (password && options.headers.Authorization !== authorization) return { ok: false, status: 401, statusText: 'Unauthorized' }
      if (url.searchParams.get('directory') !== '/workspace') return { ok: false, status: 404, statusText: 'Unknown directory' }
      return { ok: true, json: async () => snapshot }
    },
  })
  const module = new vm.SourceTextModule(await readFile(new URL('opencode-status.js', import.meta.url), 'utf8'), { context: sandbox })
  await module.link(specifier => {
    assert.equal(specifier, 'node:child_process')
    return new vm.SyntheticModule(['spawn'], function () {
      this.setExport('spawn', (_file, args) => {
        reports.push(JSON.parse(args[3]))
        const child = new EventEmitter()
        child.stderr = new EventEmitter()
        child.stderr.setEncoding = () => {}
        child.kill = () => child.emit('close')
        children.push(child)
        return child
      })
    }, { context: sandbox })
  })
  await module.evaluate()
  const options = {
    serverUrl: new URL('http://localhost:4096'), directory: '/workspace',
    client: { app: { log: async ({ body }) => warnings.push(body.message) } },
  }
  const hooks = await module.namespace.AetherStatus(options)
  return {
    reports, live, warnings, calls,
    failList(value, kind) { if (kind) errors[kind] = value; else error = value },
    dispose: () => hooks.dispose(),
    exit: () => host.emit('exit'),
    reload: () => module.namespace.AetherStatus(options),
    async advance(milliseconds = 1000) {
      const target = now + milliseconds
      while (true) {
        const timer = [...timers].sort((a, b) => a.at - b.at)[0]
        if (!timer || timer.at > target) break
        timers.delete(timer)
        now = timer.at
        timer.callback()
        await flush()
      }
      now = target
      await flush()
    },
    blockList() { responseGate = Promise.withResolvers(); return responseGate },
    async emit(type, properties) { await hooks.event({ event: { type, properties } }); await flush() },
    async drain() {
      await flush()
      while (children.length) { children.shift().emit('close'); await flush() }
    },
  }
}

function latest(h, state, requests) {
  assert.equal(h.reports.at(-1).state, state)
  assert.deepEqual(h.reports.at(-1).input_updates, [{ operation: 'replace', requests }])
}

test('V1 preserves session and kind while other sessions work', async () => {
  const h = await reporter()
  await h.emit('session.status', { sessionID: 'root', status: { type: 'busy' } })
  await h.emit('session.status', { sessionID: 'child', status: { type: 'busy' } })
  await h.emit('question.asked', { sessionID: 'root', id: 'same', questions: ['private'] })
  await h.emit('permission.asked', { sessionID: 'child', id: 'same', metadata: { secret: 'private' } })
  h.live.question = [{ sessionID: 'root', id: 'same' }]
  h.live.permission = [{ sessionID: 'child', id: 'same' }]
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  latest(h, 'working', [request('same', 'root', 'question'), request('same', 'child', 'permission')])
  await h.emit('question.replied', { sessionID: 'child', requestID: 'same', answers: ['private'] })
  await h.drain()
  latest(h, 'working', [request('same', 'root', 'question'), request('same', 'child', 'permission')])
  await h.emit('question.rejected', { sessionID: 'root', requestID: 'same' })
  await h.emit('session.deleted', { info: { id: 'child' } })
  await h.drain()
  latest(h, 'waiting', [])
  assert.ok(!JSON.stringify(h.reports).includes('private'))
})

test('V1 protected-server reconciliation removes interrupted requests only in their session', async () => {
  const h = await reporter({ password: 'test-only-secret', username: 'member' })
  await h.emit('question.asked', { sessionID: 'root', id: 'cancelled' })
  await h.emit('question.asked', { sessionID: 'child', id: 'cancelled' })
  await h.emit('permission.asked', { sessionID: 'root', id: 'live' })
  h.live.permission = [{ sessionID: 'root', id: 'live' }]
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [request('cancelled', 'child', 'question'), request('live', 'root', 'permission')])
  assert.deepEqual(h.warnings, [])
})

test('V1 reconciliation failure retains the requests and its diagnostic', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'q' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [request('q', 'root', 'question')])
  assert.match(h.warnings[0], /native API unavailable/)
})

test('V1 an idle reconciliation cannot erase a later request or hide background start', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'old' })
  const gate = h.blockList()
  await h.emit('session.idle', { sessionID: 'root' })
  await h.emit('question.asked', { sessionID: 'root', id: 'new' })
  await h.emit('session.status', { sessionID: 'child', status: { type: 'busy' } })
  gate.resolve()
  await h.drain()
  latest(h, 'working', [request('new', 'root', 'question')])
})

test('V1 unidentified asks and generic idle never create requests', async () => {
  const h = await reporter()
  await h.emit('permission.asked', { sessionID: 'root' })
  await h.emit('question.asked', { id: 'q' })
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [])
})

test('V1 retries failed lists after recovery without another native event', async () => {
  const h = await reporter({ password: 'test-only-secret', username: 'member' })
  await h.emit('session.status', { sessionID: 'child', status: { type: 'busy' } })
  await h.emit('permission.asked', { sessionID: 'root', id: 'p' })
  await h.emit('question.asked', { sessionID: 'root', id: 'q' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  latest(h, 'working', [request('p', 'root', 'permission'), request('q', 'root', 'question')])
  h.failList(undefined)
  await h.advance()
  await h.drain()
  latest(h, 'working', [])
  assert.deepEqual(h.calls.map(call => call.kind), ['permission', 'question', 'permission', 'question'])
  const reports = h.reports.length
  await h.advance(10000)
  await h.drain()
  assert.equal(h.calls.length, 4)
  assert.equal(h.reports.length, reports)
})

test('V1 repeated failures and invalid lists retain evidence until an authoritative recovery', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'q' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  const reports = h.reports.length
  await h.advance(3000)
  h.failList(undefined)
  for (const invalid of [null, {}, [null], [{ sessionID: 'root' }]]) {
    h.live.question = invalid
    await h.advance()
    await h.drain()
    latest(h, 'waiting', [request('q', 'root', 'question')])
  }
  assert.equal(h.calls.length, 8)
  assert.equal(h.reports.length, reports)
  assert.deepEqual(h.warnings, ['status reporter: native API unavailable'])
  h.live.question = []
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [])
})

test('V1 an invalid initial list preserves the request until recovery', async () => {
  const h = await reporter()
  await h.emit('permission.asked', { sessionID: 'root', id: 'p' })
  h.live.permission = { error: 'not a list' }
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [request('p', 'root', 'permission')])
  h.live.permission = []
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [])
})

test('V1 retries only failed identities, retaining live, other-session, other-kind and newer requests', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'same' })
  await h.emit('question.asked', { sessionID: 'root', id: 'live' })
  await h.emit('question.asked', { sessionID: 'child', id: 'same' })
  await h.emit('permission.asked', { sessionID: 'root', id: 'same' })
  h.live.permission = [{ sessionID: 'root', id: 'same' }]
  h.failList(new Error('questions unavailable'), 'question')
  await h.emit('session.idle', { sessionID: 'root' })
  await h.emit('question.asked', { sessionID: 'root', id: 'new' })
  await h.drain()
  h.failList(undefined, 'question')
  h.live.question = [{ sessionID: 'root', id: 'live' }, { sessionID: 'child', id: 'same' }]
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [
    request('live', 'root', 'question'),
    request('same', 'child', 'question'),
    request('same', 'root', 'permission'),
    request('new', 'root', 'question'),
  ])
  assert.deepEqual(h.calls.map(call => call.kind), ['permission', 'question', 'question'])
  await h.advance(10000)
  assert.equal(h.calls.length, 3)
})

test('V1 an in-flight retry cannot erase a later reused identity or hide background start', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'same' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  h.failList(undefined)
  const gate = h.blockList()
  await h.advance()
  await h.emit('question.asked', { sessionID: 'root', id: 'same' })
  await h.emit('session.status', { sessionID: 'child', status: { type: 'busy' } })
  await h.advance(10000)
  assert.equal(h.calls.length, 2)
  gate.resolve()
  await h.drain()
  latest(h, 'working', [request('same', 'root', 'question')])
  await h.advance(10000)
  assert.equal(h.calls.length, 2)
})

test('V1 replacement of failed evidence before retry does not sweep the fresh request', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'same' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.emit('question.asked', { sessionID: 'root', id: 'same' })
  h.failList(undefined)
  await h.advance(10000)
  await h.drain()
  latest(h, 'waiting', [request('same', 'root', 'question')])
  assert.equal(h.calls.length, 1)
})

test('V1 resolving the failed scope cancels retries without clearing another scope', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'q' })
  await h.emit('question.asked', { sessionID: 'child', id: 'q' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.emit('question.replied', { sessionID: 'root', requestID: 'q' })
  await h.advance(10000)
  await h.drain()
  latest(h, 'waiting', [request('q', 'child', 'question')])
  assert.equal(h.calls.length, 1)
})

test('V1 session deletion aborts its retry and retains unrelated evidence and execution', async () => {
  const h = await reporter()
  await h.emit('session.status', { sessionID: 'child', status: { type: 'busy' } })
  await h.emit('question.asked', { sessionID: 'root', id: 'q' })
  await h.emit('question.asked', { sessionID: 'child', id: 'q' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  h.failList(undefined)
  const gate = h.blockList()
  await h.advance()
  await h.emit('session.deleted', { info: { id: 'root' } })
  assert.equal(h.calls.at(-1).signal.aborted, true)
  gate.resolve()
  await h.advance(10000)
  await h.drain()
  latest(h, 'working', [request('q', 'child', 'question')])
  assert.equal(h.calls.length, 2)
})

test('V1 disposal for another directory does not cancel failed reconciliation', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'q' })
  h.failList(new Error('native API unavailable'))
  await h.emit('session.idle', { sessionID: 'root' })
  await h.drain()
  await h.emit('server.instance.disposed', { directory: '/another-workspace' })
  h.failList(undefined)
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [])
  assert.equal(h.calls.length, 2)
})

for (const termination of ['dispose', 'exit', 'reload', 'directory']) {
  test(`V1 ${termination} cancels delayed retries`, async () => {
    const h = await reporter()
    await h.emit('question.asked', { sessionID: 'root', id: 'q' })
    h.failList(new Error('native API unavailable'))
    await h.emit('session.idle', { sessionID: 'root' })
    await h.drain()
    const reports = h.reports.length
    if (termination === 'directory') await h.emit('server.instance.disposed', { directory: '/workspace' })
    else await h[termination]()
    h.failList(undefined)
    await h.advance(10000)
    await h.drain()
    latest(h, 'waiting', [request('q', 'root', 'question')])
    assert.equal(h.calls.length, 1)
    assert.equal(h.reports.length, reports)
  })

  test(`V1 ${termination} aborts in-flight retry and prevents queued reports`, async () => {
    const h = await reporter()
    await h.emit('question.asked', { sessionID: 'root', id: 'q' })
    h.failList(new Error('native API unavailable'))
    await h.emit('session.idle', { sessionID: 'root' })
    await h.drain()
    const reports = h.reports.length
    h.failList(undefined)
    const gate = h.blockList()
    await h.advance()
    await h.emit('session.status', { sessionID: 'child', status: { type: 'busy' } })
    if (termination === 'directory') await h.emit('server.instance.disposed', { directory: '/workspace' })
    else await h[termination]()
    assert.equal(h.calls.at(-1).signal.aborted, true)
    gate.resolve()
    await h.advance(10000)
    await h.drain()
    assert.equal(h.calls.length, 2)
    assert.equal(h.reports.length, reports)
    latest(h, 'waiting', [request('q', 'root', 'question')])
  })
}

test('V1 disposal stops the reporter backlog and ignores later native events', async () => {
  const h = await reporter()
  await h.emit('question.asked', { sessionID: 'root', id: 'q' })
  await h.emit('question.asked', { sessionID: 'child', id: 'q' })
  assert.equal(h.reports.length, 1)
  await h.dispose()
  await h.emit('session.status', { sessionID: 'child', status: { type: 'busy' } })
  await h.emit('session.idle', { sessionID: 'root' })
  await h.advance(10000)
  await h.drain()
  latest(h, undefined, [request('q', 'root', 'question')])
  assert.equal(h.reports.length, 1)
  assert.equal(h.calls.length, 0)
})
