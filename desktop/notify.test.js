'use strict'

const assert = require('node:assert/strict')
const Module = require('node:module')
const { test, beforeEach } = require('node:test')

const shown = []
let badge = 0

class FakeNotification {
  static isSupported() {
    return true
  }
  constructor(options) {
    this.options = options
    this.closed = false
  }
  on() {}
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
    return { app: { setBadgeCount: (n) => { badge = n } }, Notification: FakeNotification }
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
  notify.start('127.0.0.1:1', 'http://127.0.0.1:1/?token=t', () => {})
})

test('idle, waiting and working never notify', async () => {
  const run = lead('r1')
  await status(run, { status: 'needs-attention', state: 'idle', reason: 'agent idle' })
  await status(run, { state: 'idle', reason: 'stalled: no output for 10m' })
  await status(run, { state: 'waiting-agents', reason: 'waiting on 3 workers' })
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

test('an unseen failure needs you until the owner sees it', async () => {
  const run = lead('r5')
  await status(run, { status: 'needs-attention', state: 'failed', reason: 'agent reported failure', outcome_unseen: true })
  assert.equal(shown.length, 1)
  assert.equal(badge, 1)
  socket.onmessage({ data: JSON.stringify({ type: 'run.outcome_seen', run_id: 'r5', payload: {} }) })
  assert.ok(shown[0].closed)
  assert.equal(badge, 0)
})

test('a finished run gets one non-urgent notice outside the badge', async () => {
  const run = lead('r6')
  await status(run, { status: 'needs-attention', state: 'finished', reason: 'agent reported success', outcome_unseen: true })
  assert.equal(shown.length, 1)
  assert.equal(shown[0].options.body, 'Agent reported success, review the result')
  assert.equal(shown[0].options.silent, true)
  assert.equal(badge, 0)
  await status(run, {})
  assert.equal(shown.length, 1)
})

test("another member's run never notifies or counts", async () => {
  const run = { ...lead('r7'), member_id: 'someone-else' }
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'blocked: no credentials for the registry' })
  await status(run, { state: 'finished', reason: 'agent reported success', outcome_unseen: true })
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
