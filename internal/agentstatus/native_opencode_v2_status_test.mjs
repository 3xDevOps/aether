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
  let waiter
  let child
  let listError
  const warnings = []
  const sandbox = vm.createContext({ process, AbortController, AbortSignal, console: { error: (...args) => warnings.push(args) } })
  const source = await readFile(new URL('opencode-status-v2.js', import.meta.url), 'utf8')
  const module = new vm.SourceTextModule(source, { context: sandbox })
  await module.link(specifier => {
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
  const dispose = module.namespace.default.setup({
    location: { directory: '/workspace' },
    permission: { async list({ sessionID }) {
      if (listError) throw listError
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
  })
  return {
    reports, warnings, dispose, permissions,
    get child() { return child },
    failList(error) { listError = error },
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
