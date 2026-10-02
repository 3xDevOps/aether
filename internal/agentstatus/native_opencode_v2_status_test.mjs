import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const flush = () => new Promise(resolve => setImmediate(resolve))

async function reporter() {
  const process = new EventEmitter()
  const reports = []
  const events = []
  const permissions = new Map()
  let waiter
  let child
  const warnings = []
  const sandbox = vm.createContext({ process, AbortController, console: { error: (...args) => warnings.push(args) } })
  const source = await readFile(new URL('opencode-status-v2.js', import.meta.url), 'utf8')
  const module = new vm.SourceTextModule(source, { context: sandbox })
  await module.link(specifier => {
    if (specifier !== 'node:child_process') throw new Error(`Standalone plugin cannot resolve ${specifier}`)
    const exports = {
      spawn(_file, args) {
        const report = Object.fromEntries(args.slice(2).reduce((pairs, value, index, list) => {
          if (index % 2 === 0) pairs.push([value.slice(2), list[index + 1]])
          return pairs
        }, []))
        reports.push(report)
        child = new EventEmitter()
        child.stderr = new EventEmitter()
        child.stderr.setEncoding = () => {}
        child.kill = () => { child.killed = true; child.emit('close') }
        return child
      },
    }
    return new vm.SyntheticModule(Object.keys(exports), function () {
      for (const [name, value] of Object.entries(exports)) this.setExport(name, value)
    }, { context: sandbox })
  })
  await module.evaluate()
  const dispose = module.namespace.default.setup({
    location: { directory: '/workspace' },
    permission: { async list({ sessionID }) {
      return [...permissions.values()].filter(request => request.sessionID === sessionID)
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
    async emit(type, data, directory = '/workspace') {
      if (type === 'permission.asked') permissions.set(data.id, data)
      if (type === 'permission.replied') permissions.delete(data.requestID)
      events.push({ type, data, location: { directory } })
      waiter?.resolve()
      await flush()
    },
    async finishReport() { child.emit('close'); await flush() },
  }
}

test('V2 nested execution cannot clear outstanding permission or form waits', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.finishReport()
  await h.emit('permission.asked', { id: 'permission', sessionID: 'root' })
  await h.finishReport()
  await h.emit('session.execution.started', { sessionID: 'child' })
  await h.emit('form.created', { form: { id: 'question', sessionID: 'child' } })
  assert.deepEqual(h.reports, [
    { event: 'session.status', status: 'busy' },
    { event: 'permission.asked' },
  ])
  await h.emit('permission.replied', { requestID: 'permission', sessionID: 'root' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'question.asked' })
  await h.emit('session.execution.succeeded', { sessionID: 'root' })
  await h.emit('form.replied', { id: 'question', sessionID: 'child', answer: {} })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.status', status: 'busy' })
  await h.emit('session.execution.interrupted', { sessionID: 'child', reason: 'user' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.idle' })
})

test('V2 replies after execution ends park rather than falsely resume', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.finishReport()
  await h.emit('form.created', { form: { id: 'question', sessionID: 'root' } })
  await h.finishReport()
  await h.emit('session.execution.failed', { sessionID: 'root' })
  assert.deepEqual(h.reports.at(-1), { event: 'question.asked' })
  await h.emit('form.cancelled', { id: 'question', sessionID: 'root' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.idle' })
})

test('V2 deletion releases only that session and ignores unrelated locations', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'elsewhere' }, '/other')
  assert.deepEqual(h.reports, [])
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.finishReport()
  await h.emit('session.execution.started', { sessionID: 'child' })
  await h.emit('permission.asked', { id: 'request', sessionID: 'child' })
  await h.finishReport()
  await h.emit('session.deleted', { sessionID: 'child' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.status', status: 'busy' })
  await h.emit('session.execution.succeeded', { sessionID: 'root' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.idle' })
})

test('V2 report ordering is serialized and unload cancels queued reports', async () => {
  const h = await reporter()
  await h.emit('session.execution.started', { sessionID: 'root' })
  const first = h.child
  await h.emit('permission.asked', { id: 'request', sessionID: 'root' })
  await h.emit('permission.replied', { requestID: 'request', sessionID: 'root' })
  assert.deepEqual(h.reports, [{ event: 'session.status', status: 'busy' }])
  await h.finishReport()
  assert.deepEqual(h.reports[1], { event: 'permission.asked' })
  const active = h.child
  h.dispose()
  await flush()
  assert.equal(active.killed, true)
  assert.equal(first.killed, undefined)
  assert.equal(h.reports.length, 2, 'queued working report must not run after unload')
})

test('V2 Stop removes vanished permission waits without requiring a reply event', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.finishReport()
  await h.emit('permission.asked', { id: 'request', sessionID: 'root' })
  await h.finishReport()
  // Permission.assert's interruption finalizer removes this live request but
  // does not publish permission.replied.
  h.permissions.delete('request')
  await h.emit('session.execution.interrupted', { sessionID: 'root', reason: 'user' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.idle' })
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.status', status: 'busy' })
})

test('V2 terminal reconciliation preserves genuinely live permissions and forms', async t => {
  const h = await reporter()
  t.after(h.dispose)
  await h.emit('session.execution.started', { sessionID: 'root' })
  await h.finishReport()
  await h.emit('permission.asked', { id: 'cancelled', sessionID: 'root' })
  await h.finishReport()
  await h.emit('permission.asked', { id: 'live', sessionID: 'root' })
  await h.emit('form.created', { form: { id: 'question', sessionID: 'root' } })
  h.permissions.delete('cancelled')
  await h.emit('session.execution.interrupted', { sessionID: 'root', reason: 'user' })
  assert.deepEqual(h.reports.at(-1), { event: 'permission.asked' })
  await h.emit('permission.replied', { requestID: 'live', sessionID: 'root' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'question.asked' })
  await h.emit('form.cancelled', { id: 'question', sessionID: 'root' })
  await h.finishReport()
  assert.deepEqual(h.reports.at(-1), { event: 'session.idle' })
})
