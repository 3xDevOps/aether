import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const flush = () => new Promise(resolve => setImmediate(resolve))
const request = (id, session_id, kind) => ({ id, session_id, kind })

async function reporter() {
  const process = new EventEmitter()
  const reports = []
  const events = []
  const permissions = new Map()
  const listCalls = []
  const listResults = []
  const timers = new Set()
  let now = 0
  let waiter
  let child
  let listError
  let listResponse
  let overrideList = false
  const warnings = []
  function timer(milliseconds, kind, fire) {
    const entry = { at: now + milliseconds, kind, fire }
    timers.add(entry)
    return entry
  }
  const sandbox = vm.createContext({
    process, AbortController,
    AbortSignal: {
      any: signals => AbortSignal.any(signals),
      timeout: milliseconds => {
        const controller = new AbortController()
        timer(milliseconds, 'timeout', () => controller.abort(new Error('permission list timed out')))
        return controller.signal
      },
    },
    console: { error: (...args) => warnings.push(args) },
  })
  const source = await readFile(new URL('opencode-status-v2.js', import.meta.url), 'utf8')
  const module = new vm.SourceTextModule(source, { context: sandbox })
  await module.link(specifier => {
    if (specifier === 'node:timers/promises') {
      return new vm.SyntheticModule(['setTimeout'], function () {
        this.setExport('setTimeout', (milliseconds, value, { signal, ref }) => {
          assert.equal(ref, false)
          return new Promise((resolve, reject) => {
            const abort = () => {
              timers.delete(entry)
              reject(signal.reason)
            }
            const entry = timer(milliseconds, 'retry', () => {
              signal.removeEventListener('abort', abort)
              resolve(value)
            })
            signal.addEventListener('abort', abort, { once: true })
            if (signal.aborted) abort()
          })
        })
      }, { context: sandbox })
    }
    assert.equal(specifier, 'node:child_process')
    return new vm.SyntheticModule(['spawn'], function () {
      this.setExport('spawn', (_file, args) => {
        reports.push(JSON.parse(args[3]))
        child = new EventEmitter()
        child.stderr = new EventEmitter()
        child.stderr.setEncoding = () => {}
        child.kill = () => { child.killed = true; child.closed = true; child.emit('close') }
        return child
      })
    }, { context: sandbox })
  })
  await module.evaluate()
  const context = {
    location: { directory: '/workspace' },
    permission: { async list({ sessionID }, { signal }) {
      listCalls.push({ sessionID, signal })
      if (listResults.length) return listResults.shift()(signal)
      if (listError) throw listError
      if (overrideList) return listResponse
      return [...permissions.values()].filter(item => item.sessionID === sessionID)
    } },
    event: { async *subscribe({ signal }) {
      while (!signal.aborted) {
        if (events.length) { yield events.shift(); continue }
        waiter = Promise.withResolvers()
        const resume = waiter.resolve
        signal.addEventListener('abort', resume, { once: true })
        await waiter.promise
        signal.removeEventListener('abort', resume)
      }
    } },
  }
  let dispose = module.namespace.default.setup(context)
  return {
    reports, warnings, permissions, listCalls,
    dispose() { dispose() },
    reload() { dispose = module.namespace.default.setup(context) },
    exit() { process.emit('exit') },
    get retryCount() { return [...timers].filter(entry => entry.kind === 'retry').length },
    get child() { return child },
    failList(error) { listError = error },
    respondList(value) { overrideList = true; listResponse = value },
    recoverList() { listError = undefined; overrideList = false },
    deferList({ ignoreAbort = false } = {}) {
      const result = Promise.withResolvers()
      listResults.push(async signal => {
        const abort = () => result.reject(signal.reason)
        if (!ignoreAbort) signal.addEventListener('abort', abort, { once: true })
        try { return await result.promise }
        finally { signal.removeEventListener('abort', abort) }
      })
      return result
    },
    async advance(milliseconds = 1000) {
      now += milliseconds
      for (const entry of [...timers]) {
        if (entry.at > now) continue
        timers.delete(entry)
        entry.fire()
      }
      await flush()
    },
    async emit(type, data, directory = '/workspace') {
      if (type === 'permission.asked') permissions.set(JSON.stringify([data.sessionID, data.id]), data)
      if (type === 'permission.replied') permissions.delete(JSON.stringify([data.sessionID, data.requestID]))
      events.push({ type, data, location: { directory } })
      waiter?.resolve()
      await flush()
    },
    async drain() {
      await flush()
      while (child && !child.closed) {
        child.closed = true
        child.emit('close')
        await flush()
      }
    },
  }
}

function latest(h, state, requests) {
  assert.equal(h.reports.at(-1).state, state)
  assert.deepEqual(h.reports.at(-1).input_updates, [{ operation: 'replace', requests }])
}

test('V2 execution and multiple correlated requests remain independent', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.emit('permission.asked', { id: 'same', sessionID: 'root', metadata: { password: 'secret' } })
  await h.emit('session.execution.started', { sessionID: 'child' })
  await h.emit('form.created', { form: { id: 'same', sessionID: 'child', title: 'private', fields: [] } })
  await h.drain()
  latest(h, 'working', [request('same', 'root', 'permission'), request('same', 'child', 'form')])
  await h.emit('session.execution.succeeded', { sessionID: 'root' })
  await h.emit('form.replied', { id: 'same', sessionID: 'unrelated', answer: { token: 'private' } })
  await h.drain()
  latest(h, 'working', [request('same', 'root', 'permission'), request('same', 'child', 'form')])
  await h.emit('permission.replied', { requestID: 'same', sessionID: 'root' })
  await h.emit('session.execution.failed', { sessionID: 'child' })
  await h.drain()
  latest(h, 'waiting', [request('same', 'child', 'form')])
  await h.emit('form.cancelled', { id: 'same', sessionID: 'child' })
  await h.drain()
  latest(h, 'waiting', [])
  assert.ok(!JSON.stringify(h.reports).includes('private'))
  assert.ok(!JSON.stringify(h.reports).includes('secret'))
})

test('V2 interruption reconciles only vanished permissions, not live forms', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.emit('permission.asked', { id: 'gone', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'live', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'gone', sessionID: 'child' })
  await h.emit('form.created', { form: { id: 'f', sessionID: 'root' } })
  h.permissions.delete(JSON.stringify(['root', 'gone']))
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [request('live', 'root', 'permission'), request('gone', 'child', 'permission'), request('f', 'root', 'form')])
  await h.emit('session.deleted', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [request('gone', 'child', 'permission')])
})

test('V2 failed reconciliation preserves request evidence and reports the error', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'p', sessionID: 'root' })
  h.failList(new Error('permission list unavailable'))
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [request('p', 'root', 'permission')])
  assert.match(String(h.warnings), /permission list unavailable/)
})

test('V2 ignores other locations and unidentified requests', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'other' }, '/other')
  assert.deepEqual(h.reports, [])
  await h.emit('permission.asked', { sessionID: 'root' })
  await h.emit('form.created', { form: { id: 'f' } })
  await h.drain()
  assert.deepEqual(h.reports, [])
})

test('V2 serializes snapshots and unload cancels queued reporting', async () => {
  const h = await reporter()
  await h.emit('session.execution.started', { sessionID: 'root' })
  const first = h.child
  await h.emit('permission.asked', { id: 'p', sessionID: 'root' })
  await h.emit('permission.replied', { requestID: 'p', sessionID: 'root' })
  assert.equal(h.reports.length, 1)
  first.closed = true
  first.emit('close')
  await flush()
  latest(h, 'working', [request('p', 'root', 'permission')])
  const active = h.child
  h.dispose()
  await flush()
  assert.equal(active.killed, true)
  assert.equal(first.killed, undefined)
  assert.equal(h.reports.length, 2)
})

test('V2 input-only callbacks preserve unknown execution rather than invent a turn', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('form.created', { form: { id: 'f', sessionID: 'root' } })
  await h.drain()
  latest(h, undefined, [request('f', 'root', 'form')])
  await h.emit('form.replied', { id: 'f', sessionID: 'root', answer: {} })
  await h.drain()
  latest(h, undefined, [])
})

test('V2 failed terminal reconciliation recovers without an event and stops polling live requests', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.emit('session.execution.started', { sessionID: 'child' })
  await h.emit('permission.asked', { id: 'gone', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'live', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'gone', sessionID: 'child' })
  await h.emit('form.created', { form: { id: 'gone', sessionID: 'root' } })
  h.permissions.delete(JSON.stringify(['root', 'gone']))
  h.failList(new Error('native permission service unavailable'))
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.drain()
  latest(h, 'working', [
    request('gone', 'root', 'permission'), request('live', 'root', 'permission'),
    request('gone', 'child', 'permission'), request('gone', 'root', 'form'),
  ])
  h.recoverList()
  await h.advance()
  await h.drain()
  latest(h, 'working', [
    request('live', 'root', 'permission'), request('gone', 'child', 'permission'),
    request('gone', 'root', 'form'),
  ])
  assert.deepEqual(h.listCalls.map(call => call.sessionID), ['root', 'root'])
  assert.equal(h.retryCount, 0)
  const reports = h.reports.length
  h.permissions.clear()
  await h.advance(10000)
  await h.drain()
  assert.equal(h.listCalls.length, 2)
  assert.equal(h.reports.length, reports)
})

test('V2 permission replies retry failed cleanup without inventing execution', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'reply', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'gone', sessionID: 'root' })
  h.permissions.delete(JSON.stringify(['root', 'gone']))
  h.failList(new Error('permission list unavailable'))
  await h.emit('permission.replied', { requestID: 'reply', sessionID: 'root' })
  await h.drain()
  latest(h, undefined, [request('gone', 'root', 'permission')])
  h.recoverList()
  await h.advance()
  await h.drain()
  latest(h, undefined, [])
  assert.equal(h.retryCount, 0)
})

test('V2 repeated failures and malformed authoritative lists retain evidence until recovery', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'p', sessionID: 'root' })
  h.failList(new Error('permission service offline'))
  await h.emit('session.execution.failed', { sessionID: 'root' })
  await h.drain()
  const reports = h.reports.length
  await h.advance()
  latest(h, 'waiting', [request('p', 'root', 'permission')])
  assert.equal(h.retryCount, 1)
  h.recoverList()
  for (const invalid of [null, { data: [] }, [null], [{ id: 'p', sessionID: 'other' }], [{ id: 'live', sessionID: 'root' }, {}]]) {
    h.respondList(invalid)
    await h.advance()
    await h.drain()
    latest(h, 'waiting', [request('p', 'root', 'permission')])
    assert.equal(h.retryCount, 1)
    assert.equal(h.reports.length, reports)
  }
  assert.match(String(h.warnings), /permission service offline/)
  assert.equal(h.warnings.length, 1)
  h.respondList([])
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [])
  assert.equal(h.retryCount, 0)
})

test('V2 an invalid initial response preserves the request until recovery', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'p', sessionID: 'root' })
  h.respondList({})
  await h.emit('session.execution.succeeded', { sessionID: 'root' })
  await h.drain()
  latest(h, 'waiting', [request('p', 'root', 'permission')])
  h.respondList([])
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [])
})

test('V2 recovery does not overlap reads or erase requests newer than its snapshot', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'gone', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'renewed', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'gone', sessionID: 'child' })
  await h.emit('form.created', { form: { id: 'gone', sessionID: 'root' } })
  h.failList(new Error('permission list unavailable'))
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.drain()
  h.recoverList()
  const read = h.deferList()
  await h.advance()
  await h.emit('permission.asked', { id: 'renewed', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'new', sessionID: 'root' })
  await h.emit('session.execution.succeeded', { sessionID: 'root' })
  await h.emit('session.execution.failed', { sessionID: 'root' })
  await h.advance()
  assert.equal(h.listCalls.length, 2)
  read.resolve([])
  await h.drain()
  latest(h, 'waiting', [
    request('renewed', 'root', 'permission'), request('gone', 'child', 'permission'),
    request('gone', 'root', 'form'), request('new', 'root', 'permission'),
  ])
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [
    request('renewed', 'root', 'permission'), request('gone', 'child', 'permission'),
    request('gone', 'root', 'form'), request('new', 'root', 'permission'),
  ])
  assert.equal(h.listCalls.length, 3)
  assert.equal(h.retryCount, 0)
})

test('V2 the four-second query bound retains evidence and schedules recovery', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'gone', sessionID: 'root' })
  h.permissions.clear()
  h.deferList()
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.drain()
  await h.advance(3999)
  assert.equal(h.listCalls[0].signal.aborted, false)
  latest(h, 'waiting', [request('gone', 'root', 'permission')])
  await h.advance(1)
  assert.equal(h.listCalls[0].signal.aborted, true)
  assert.match(String(h.warnings), /permission list timed out/)
  assert.equal(h.retryCount, 1)
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [])
})

test('V2 resolving the last failed identity cancels recovery, not newer input', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'old', sessionID: 'root' })
  h.failList(new Error('permission list unavailable'))
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.drain()
  await h.emit('permission.replied', { requestID: 'old', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'new', sessionID: 'root' })
  h.recoverList()
  h.permissions.clear()
  await h.advance(10000)
  await h.drain()
  latest(h, 'waiting', [request('new', 'root', 'permission')])
  assert.equal(h.listCalls.length, 1)
  assert.equal(h.retryCount, 0)
})

for (const ending of ['session.deleted', 'location.shutdown', 'dispose', 'reload', 'exit']) {
  for (const inFlight of [false, true]) {
    test(`V2 ${ending} cancels ${inFlight ? 'in-flight' : 'delayed'} permission recovery`, async t => {
      const h = await reporter()
      t.after(h.dispose)
      await h.emit('permission.asked', { id: 'p', sessionID: 'root' })
      await h.emit('permission.asked', { id: 'p', sessionID: 'child' })
      await h.emit('form.created', { form: { id: 'f', sessionID: 'child' } })
      h.failList(new Error('permission list unavailable'))
      await h.emit('session.execution.interrupted', { sessionID: 'root' })
      await h.drain()
      h.recoverList()
      let read
      if (inFlight) {
        read = h.deferList({ ignoreAbort: true })
        await h.advance()
      }
      if (ending === 'session.deleted' || ending === 'location.shutdown') {
        await h.emit(ending, { sessionID: 'root' })
      } else {
        h[ending]()
      }
      await h.drain()
      assert.equal(h.retryCount, 0)
      assert.equal(h.listCalls.at(-1).signal.aborted, true)
      if (ending === 'session.deleted') {
        latest(h, 'waiting', [request('p', 'child', 'permission'), request('f', 'child', 'form')])
        await h.emit('permission.asked', { id: 'p', sessionID: 'root' })
        await h.drain()
      }
      const reports = h.reports.length
      const calls = h.listCalls.length
      read?.resolve([])
      await h.advance(10000)
      await h.drain()
      assert.equal(h.listCalls.length, calls)
      assert.equal(h.reports.length, reports)
      assert.equal(h.retryCount, 0)
      if (ending === 'session.deleted') {
        latest(h, 'waiting', [
          request('p', 'child', 'permission'), request('f', 'child', 'form'),
          request('p', 'root', 'permission'),
        ])
      }
    })
  }
}

test('V2 replacing the only failed request retires its delayed reconciliation', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'same', sessionID: 'root' })
  h.failList(new Error('permission list unavailable'))
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.drain()
  await h.emit('permission.asked', { id: 'same', sessionID: 'root' })
  h.recoverList()
  h.permissions.clear()
  await h.advance(10000)
  await h.drain()
  latest(h, 'waiting', [request('same', 'root', 'permission')])
  assert.equal(h.listCalls.length, 1)
  assert.equal(h.retryCount, 0)
})

test('V2 deleting one failed session does not cancel another session recovery', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('permission.asked', { id: 'same', sessionID: 'root' })
  await h.emit('permission.asked', { id: 'same', sessionID: 'child' })
  await h.emit('form.created', { form: { id: 'same', sessionID: 'child' } })
  h.failList(new Error('permission list unavailable'))
  await h.emit('session.execution.interrupted', { sessionID: 'root' })
  await h.emit('session.execution.interrupted', { sessionID: 'child' })
  await h.drain()
  await h.emit('session.deleted', { sessionID: 'root' })
  h.recoverList()
  h.permissions.clear()
  await h.advance()
  await h.drain()
  latest(h, 'waiting', [request('same', 'child', 'form')])
  assert.deepEqual(h.listCalls.map(call => call.sessionID), ['root', 'child', 'child'])
  assert.equal(h.retryCount, 0)
})
