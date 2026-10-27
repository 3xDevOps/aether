'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const Module = require('node:module')
const os = require('node:os')
const path = require('node:path')
const { test, beforeEach, after } = require('node:test')

const shown = []
let badge = 0
let supported = true
let focused = null
const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'aether-notify-'))
after(() => fs.rmSync(userData, { recursive: true, force: true }))

class FakeNotification {
  static isSupported() {
    return supported
  }
  constructor(options) {
    this.options = options
    this.closed = false
    this.handlers = {}
  }
  on(name, handler) {
    this.handlers[name] = handler
  }
  show() {
    shown.push(this)
  }
  close() {
    this.closed = true
  }
}

const load = Module._load
Module._load = function (request, ...rest) {
  if (request === 'electron') {
    return {
      app: { setBadgeCount: (n) => { badge = n }, getPath: () => userData },
      BrowserWindow: { getFocusedWindow: () => focused },
      Notification: FakeNotification,
    }
  }
  return load.call(this, request, ...rest)
}
const notify = require('./notify')

let socket = null
globalThis.WebSocket = class {
  constructor() {
    socket = this
  }
  send() {}
  close() {}
}

const runs = new Map()
let readFails = false
globalThis.fetch = async (url, init) => {
  if (url.endsWith('/server.info')) return { ok: true, json: async () => ({ member: { id: 'me' } }) }
  const run = readFails ? undefined : runs.get(JSON.parse(init.body).run_id)
  return { ok: Boolean(run), status: 500, json: async () => ({ run }) }
}

let seq = 0

async function status(run, fields) {
  Object.assign(run, fields, { status_seq: ++seq })
  runs.set(run.id, { ...run })
  socket.onmessage({
    data: JSON.stringify({
      type: 'run.status',
      run_id: run.id,
      payload: {
        to: run.status,
        reason: run.reason,
        state: run.state,
        status_seq: run.status_seq,
        outcome_unseen: run.outcome_unseen,
        review: run.review,
      },
    }),
  })
  await new Promise((resolve) => setImmediate(resolve))
}

function lead(id) {
  return { id, member_id: 'me', title: `run ${id}`, status: 'running', state: 'working', reason: '', status_changed_at: '2026-10-16T00:00:00Z' }
}

beforeEach(() => {
  shown.length = 0
  readFails = false
  supported = true
  focused = null
  notify.start('127.0.0.1:1', 'http://127.0.0.1:1/?token=t', () => {})
})

async function review(run, value) {
  Object.assign(run, { review: value, status_seq: ++seq })
  runs.set(run.id, { ...run })
  socket.onmessage({ data: JSON.stringify({ type: 'run.review', run_id: run.id, payload: { review: value, status_seq: run.status_seq } }) })
  await new Promise((resolve) => setImmediate(resolve))
}

test('waiting and working never notify', async () => {
  const run = lead('r1')
  await status(run, { status: 'needs-attention', state: 'idle', reason: 'agent idle' })
  await status(run, { state: 'waiting', reason: 'turn ended without a report' })
  await status(run, { state: 'waiting', reason: 'no update for 30m' })
  await status(run, { state: 'waiting-agents', reason: 'waiting on 3 workers' })
  await status(run, { state: 'waiting-ci', reason: 'CI on PR 12' })
  await status(run, { status: 'running', state: 'waiting-tools', reason: '' })
  await status(run, { state: 'working' })
  assert.equal(shown.length, 0)
  assert.equal(badge, 0)
})

test('a need notifies once with the server reason, and clears when it ends', async () => {
  const run = lead('r2')
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'blocked: no credentials for the registry' })
  assert.equal(shown.length, 1)
  assert.equal(shown[0].options.title, 'run r2')
  assert.equal(shown[0].options.body, 'Blocked: no credentials for the registry')
  assert.equal(badge, 1)

  await status(run, {})
  assert.equal(shown.length, 1, 'a status_seq bump on the same need stays quiet')

  await status(run, { status: 'running', state: 'working', reason: '' })
  assert.ok(shown[0].closed)
  assert.equal(badge, 0)
})

test('a paused run and a worker that is not blocked never notify', async () => {
  const paused = lead('r3')
  paused.paused = true
  await status(paused, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })

  const worker = lead('r4')
  worker.mission_role = 'worker'
  await status(worker, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })
  assert.equal(shown.length, 0)

  await status(worker, { reason: 'blocked: tests need Docker', status_changed_at: '2026-10-16T00:01:00Z' })
  assert.equal(shown.length, 1)
  assert.equal(shown[0].options.body, 'Worker blocked: tests need Docker')
})

test('an interrupted or stopped turn asks for a message; a cancelled one stays quiet', async () => {
  const cancelled = lead('r10')
  await status(cancelled, { status: 'needs-attention', state: 'idle', reason: 'turn cancelled' })
  assert.equal(shown.length, 0)

  const interrupted = lead('r11')
  await status(interrupted, { status: 'needs-attention', state: 'needs-you', reason: 'turn interrupted' })
  const stopped = lead('r12')
  await status(stopped, { status: 'needs-attention', state: 'needs-you', reason: 'turn ended: max_tokens' })
  const stranded = lead('r13')
  await status(stranded, { status: 'needs-attention', state: 'needs-you', cause: 'wait-fault', reason: 'wait broken: run:run-1:finished: resolved at Oct 27 01:40 UTC and the agent was not woken' })
  assert.deepEqual(shown.map((n) => n.options.body), [
    'Turn interrupted, send a message to continue',
    'Turn ended (max_tokens), send a message to continue',
    'Wait broken, send a message to continue: run:run-1:finished: resolved at Oct 27 01:40 UTC and the agent was not woken',
  ])
})

test('an unseen failure needs you until the owner sees it', async () => {
  const run = lead('r5')
  await status(run, { status: 'needs-attention', state: 'failed', reason: 'agent reported failure', outcome_unseen: true })
  assert.equal(shown.length, 1)
  assert.equal(badge, 1)
  socket.onmessage({ data: JSON.stringify({ type: 'run.outcome_seen', run_id: 'r5', payload: {} }) })
  assert.ok(shown[0].closed)
  assert.equal(badge, 0)
})

test('a success ready for review notifies once, quietly, until it is put off', async () => {
  const run = lead('r6')
  await status(run, { status: 'needs-attention', state: 'finished', reason: 'agent reported success', outcome_unseen: true, review: 'ready' })
  assert.equal(shown.length, 1)
  assert.equal(shown[0].options.body, 'Agent reported success, review the result')
  assert.equal(shown[0].options.silent, true)
  assert.equal(badge, 1)
  await status(run, {})
  assert.equal(shown.length, 1)

  await review(run, 'later')
  assert.ok(shown[0].closed)
  assert.equal(badge, 0)
  await status(run, {})
  assert.equal(shown.length, 1, 'a success put off for later stays quiet')

  await status(run, { status: 'running', state: 'working', reason: '', review: '' })
  await status(run, { status: 'needs-attention', state: 'finished', reason: 'agent reported success', review: 'ready', status_changed_at: '2026-10-16T00:02:00Z' })
  assert.equal(badge, 1, 'a new success report asks again')
})

test('a completed success notifies until accepted; an old finished run never does', async () => {
  const run = lead('r9')
  await status(run, { status: 'completed', state: '', reason: 'agent reported success', outcome_unseen: true, review: 'ready' })
  assert.equal(shown.length, 1)
  await review(run, 'accepted')
  assert.ok(shown[0].closed)
  assert.equal(badge, 0)

  const old = lead('r10')
  await status(old, { status: 'needs-attention', state: 'finished', reason: 'agent reported success', outcome_unseen: true })
  assert.equal(shown.length, 1)
})

test('a success put off while its run is read never notifies', async () => {
  const run = lead('r11')
  const read = status(run, { status: 'completed', state: '', reason: 'agent reported success', outcome_unseen: true, review: 'ready' })
  await review(run, 'later')
  await read
  assert.equal(shown.length, 0)
  assert.equal(badge, 0)
})

test("another member's run never notifies or counts", async () => {
  const run = { ...lead('r7'), member_id: 'someone-else' }
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'blocked: no credentials for the registry' })
  await status(run, { state: 'finished', reason: 'agent reported success', outcome_unseen: true, review: 'ready' })
  assert.equal(shown.length, 0)
  assert.equal(badge, 0)
})

test('a failed read announces nothing, and the next read announces the need once', async () => {
  const run = lead('r8')
  readFails = true
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })
  assert.equal(shown.length, 0)
  assert.equal(badge, 0)

  readFails = false
  await status(run, {})
  assert.equal(shown.length, 1)
  assert.equal(shown[0].options.body, 'pick a database')
  assert.equal(badge, 1)
  await status(run, {})
  assert.equal(shown.length, 1)
})

test('an update notifies once per key, across a sidecar respawn', () => {
  let clicks = 0
  const update = { key: 'cli:v1.2.0', title: 'Aether 1.2.0 is available', body: 'Run aether update' }
  notify.showUpdate(update, () => clicks++)
  assert.equal(shown.length, 1)
  assert.equal(shown[0].options.title, 'Aether 1.2.0 is available')
  assert.equal(shown[0].options.body, 'Run aether update')
  shown[0].handlers.click()
  assert.equal(clicks, 1)

  notify.start('127.0.0.1:1', 'http://127.0.0.1:1/?token=t', () => {})
  notify.showUpdate(update, () => {})
  assert.equal(shown.length, 1)
  notify.showUpdate({ ...update, key: 'server:v1.2.0' }, () => {})
  assert.equal(shown.length, 2)
  notify.showUpdate({ ...update, key: 'cli:v1.3.0' }, () => {})
  assert.equal(shown.length, 3)
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(userData, 'announced-updates.json'), 'utf8')), [
    'cli:v1.2.0',
    'server:v1.2.0',
    'cli:v1.3.0',
  ])
})

test('a focused window gets no update notification, and a later unfocused call still does', () => {
  const update = { key: 'cli:v2.0.0', title: 'Aether 2.0.0 is available', body: '' }
  focused = {}
  notify.showUpdate(update, () => {})
  assert.equal(shown.length, 0)
  focused = null
  notify.showUpdate(update, () => {})
  assert.equal(shown.length, 1)
})

test('no update notification where notifications are unsupported', () => {
  supported = false
  notify.showUpdate({ key: 'cli:v3.0.0', title: 'Aether 3.0.0 is available', body: '' }, () => {})
  assert.equal(shown.length, 0)
})
