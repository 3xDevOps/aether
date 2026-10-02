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
  let error
  let responseGate
  const sandbox = vm.createContext({
    URL, Buffer, AbortSignal,
    process: { env: { OPENCODE_SERVER_PASSWORD: password, OPENCODE_SERVER_USERNAME: username } },
    fetch: async (url, options) => {
      if (responseGate) await responseGate.promise
      if (error) throw error
      const authorization = `Basic ${Buffer.from(`${username}:${password}`).toString('base64')}`
      if (password && options.headers.Authorization !== authorization) return { ok: false, status: 401, statusText: 'Unauthorized' }
      if (url.searchParams.get('directory') !== '/workspace') return { ok: false, status: 404, statusText: 'Unknown directory' }
      return { ok: true, json: async () => live[url.pathname.slice(1)] }
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
        children.push(child)
        return child
      })
    }, { context: sandbox })
  })
  await module.evaluate()
  const hooks = await module.namespace.AetherStatus({
    serverUrl: new URL('http://localhost:4096'), directory: '/workspace',
    client: { app: { log: async ({ body }) => warnings.push(body.message) } },
  })
  return {
    reports, live, warnings,
    failList(value) { error = value },
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
