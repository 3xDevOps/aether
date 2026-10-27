'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const Module = require('node:module')
const os = require('node:os')
const path = require('node:path')
const { test, beforeEach, after, mock } = require('node:test')

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
const watches = new Map()
globalThis.fetch = async (url, init) => {
  if (url.endsWith('/server.info')) return { ok: true, json: async () => ({ member: { id: 'me' } }) }
  if (url.endsWith('/watch.get')) {
    const got = watches.get(JSON.parse(init.body).watch_id)
    return { ok: Boolean(got), status: 404, json: async () => got }
  }
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

let linked = []

beforeEach(() => {
  linked = []
  watches.clear()
  shown.length = 0
  readFails = false
  supported = true
  focused = null
  notify.start('127.0.0.1:1', 'http://127.0.0.1:1/?token=t', () => {}, (link) => linked.push(link))
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
  assert.deepEqual(shown.map((n) => n.options.body), [
    'Turn interrupted, send a message to continue',
    'Turn ended (max_tokens), send a message to continue',
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

async function watchChanged(payload) {
  socket.onmessage({ data: JSON.stringify({ type: 'watch.changed', payload }) })
  await new Promise((resolve) => setImmediate(resolve))
  await new Promise((resolve) => setImmediate(resolve))
}

function watchWith(id, target, notice) {
  watches.set(id, {
    watch: { id, target, link: '/?page=settings&id=watches&watch=' + id },
    notifications: [{ id: 'not-1', watch_id: id, title: 'PR #12 merged on GitHub', body: 'acme/app', link: 'https://github.com/acme/app/pull/12', ...notice }],
  })
}

test("a watch notice shows natively and opens its link; another member's never does", async () => {
  watchWith('wat-1', { kind: 'pr', repo: 'acme/app', number: 12 }, { condition: 'merged' })
  await watchChanged({ watch_id: 'wat-1', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 1)
  assert.equal(shown[0].options.title, 'PR #12 merged on GitHub')
  assert.equal(shown[0].options.body, 'acme/app')
  shown[0].handlers.click()
  assert.deepEqual(linked, ['https://github.com/acme/app/pull/12'])

  await watchChanged({ watch_id: 'wat-1', member_id: 'someone-else', notification_id: 'not-1' })
  await watchChanged({ watch_id: 'wat-1', member_id: 'me' })
  assert.equal(shown.length, 1, 'only a new notice for this member shows')
})

test('a watched need and the same run.status need show once', async () => {
  const run = lead('r9')
  watchWith('wat-2', { kind: 'run', run_id: 'r9' }, { condition: 'needs_you', title: 'run r9 needs you', link: '/?run=r9' })
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })
  await watchChanged({ watch_id: 'wat-2', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 1)
})

test('a live failure and its watched need show once, in either order', async () => {
  const first = lead('r17')
  watchWith('wat-7', { kind: 'run', run_id: 'r17' }, { condition: 'needs_you', title: 'run r17 failed', link: '/?run=r17' })
  await status(first, { status: 'needs-attention', state: 'failed', reason: 'agent reported failure', outcome_unseen: true })
  await watchChanged({ watch_id: 'wat-7', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 1)

  const second = lead('r18')
  watchWith('wat-8', { kind: 'run', run_id: 'r18' }, { condition: 'needs_you', title: 'run r18 failed', link: '/?run=r18' })
  await watchChanged({ watch_id: 'wat-8', member_id: 'me', notification_id: 'not-1' })
  await status(second, { status: 'needs-attention', state: 'failed', reason: 'agent reported failure', outcome_unseen: true })
  assert.equal(shown.length, 2)
})

test('a success ready for review and its watched finish show once, in either order', async () => {
  const first = lead('r13')
  watchWith('wat-3', { kind: 'run', run_id: 'r13' }, { condition: 'finished', title: 'Agent reported done', link: '/?run=r13' })
  await status(first, { status: 'needs-attention', state: 'finished', reason: 'agent reported success', outcome_unseen: true, review: 'ready' })
  await watchChanged({ watch_id: 'wat-3', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 1)

  const second = lead('r16')
  watchWith('wat-6', { kind: 'run', run_id: 'r16' }, { condition: 'finished', title: 'Agent reported done', link: '/?run=r16' })
  await watchChanged({ watch_id: 'wat-6', member_id: 'me', notification_id: 'not-1' })
  await status(second, { status: 'needs-attention', state: 'finished', reason: 'agent reported success', outcome_unseen: true, review: 'ready' })
  assert.equal(shown.length, 2)
  assert.equal(shown[1].options.title, 'Agent reported done')
})

test('a watched need shown before its run.status closes when the need is answered', async () => {
  const run = lead('r14')
  watchWith('wat-4', { kind: 'run', run_id: 'r14' }, { condition: 'needs_you', title: 'run r14 needs you', link: '/?run=r14' })
  await watchChanged({ watch_id: 'wat-4', member_id: 'me', notification_id: 'not-1' })
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })
  assert.equal(shown.length, 1)
  assert.equal(badge, 1)

  await status(run, { status: 'running', state: 'working', reason: '' })
  assert.ok(shown[0].closed)
  assert.equal(badge, 0)
})

test('a watched need shown after its run.status closes with it', async () => {
  const run = lead('r15')
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })
  watchWith('wat-5', { kind: 'run', run_id: 'r15' }, { condition: 'needs_you', title: 'run r15 needs you', link: '/?run=r15' })
  const now = Date.now()
  mock.method(Date, 'now', () => now + 6000)
  await watchChanged({ watch_id: 'wat-5', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 2)

  await status(run, { status: 'running', state: 'working', reason: '' })
  assert.ok(shown.every((n) => n.closed))
  mock.restoreAll()
})

test('a finish right after a need still shows, and stays until its outcome is seen', async () => {
  const run = lead('r17')
  await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })
  watchWith('wat-7', { kind: 'run', run_id: 'r17' }, { condition: 'finished', title: 'Stopped', link: '/?run=r17' })
  await watchChanged({ watch_id: 'wat-7', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 2)
  await status(run, { status: 'killed', state: 'finished', reason: 'killed' })
  assert.ok(shown[0].closed, 'the need ended')
  assert.ok(!shown[1].closed, 'the finish stays')
  socket.onmessage({ data: JSON.stringify({ type: 'run.outcome_seen', run_id: 'r17', payload: {} }) })
  assert.ok(shown[1].closed)
})

test('a watched finish shown before its run.status closes when the success is put off', async () => {
  const run = lead('r18')
  watchWith('wat-8', { kind: 'run', run_id: 'r18' }, { condition: 'finished', title: 'Agent reported done', link: '/?run=r18' })
  await watchChanged({ watch_id: 'wat-8', member_id: 'me', notification_id: 'not-1' })
  await status(run, { status: 'needs-attention', state: 'finished', reason: 'agent reported success', outcome_unseen: true, review: 'ready' })
  assert.equal(shown.length, 1)
  await review(run, 'later')
  assert.ok(shown[0].closed)
})

test("an assistant-made watch's notice is left to its assistant.notice", async () => {
  watches.set('wat-9', {
    watch: { id: 'wat-9', source: 'assistant', target: { kind: 'run', run_id: 'r19' }, link: '/?run=r19' },
    notifications: [{ id: 'not-1', watch_id: 'wat-9', condition: 'finished', title: 'Agent reported done', body: '', link: '/?run=r19' }],
  })
  await watchChanged({ watch_id: 'wat-9', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 0)
})

test('a lost connection or a stop closes the watch notices it can no longer end', async () => {
  watchWith('wat-10', { kind: 'run', run_id: 'r20' }, { condition: 'needs_you', title: 'run r20 needs you', link: '/?run=r20' })
  await watchChanged({ watch_id: 'wat-10', member_id: 'me', notification_id: 'not-1' })
  watchWith('wat-11', { kind: 'run', run_id: 'r21' }, { condition: 'finished', title: 'Agent reported done', link: '/?run=r21' })
  await watchChanged({ watch_id: 'wat-11', member_id: 'me', notification_id: 'not-1' })
  assert.equal(shown.length, 2)
  socket.onclose()
  assert.ok(shown.every((n) => n.closed))

  notify.start('127.0.0.1:1', 'http://127.0.0.1:1/?token=t', () => {}, () => {})
  watchWith('wat-12', { kind: 'run', run_id: 'r22' }, { condition: 'needs_you', title: 'run r22 needs you', link: '/?run=r22' })
  await watchChanged({ watch_id: 'wat-12', member_id: 'me', notification_id: 'not-1' })
  notify.stop()
  assert.ok(shown[2].closed)
})

test('a watched need or finish that ends while its watch is read never shows', async () => {
  const fetched = globalThis.fetch
  let release
  const held = new Promise((resolve) => { release = resolve })
  globalThis.fetch = async (url, init) => {
    if (url.endsWith('/watch.get')) await held
    return fetched(url, init)
  }
  try {
    const run = lead('r23')
    await status(run, { status: 'needs-attention', state: 'needs-you', reason: 'needs input: pick a database' })
    watchWith('wat-13', { kind: 'run', run_id: 'r23' }, { condition: 'needs_you', title: 'run r23 needs you', link: '/?run=r23' })
    watchWith('wat-14', { kind: 'run', run_id: 'r24' }, { condition: 'finished', title: 'Agent reported done', link: '/?run=r24' })
    const now = Date.now()
    mock.method(Date, 'now', () => now + 6000)
    const pending = Promise.all([
      watchChanged({ watch_id: 'wat-13', member_id: 'me', notification_id: 'not-1' }),
      watchChanged({ watch_id: 'wat-14', member_id: 'me', notification_id: 'not-1' }),
    ])
    await status(run, { status: 'running', state: 'working', reason: '' })
    socket.onmessage({ data: JSON.stringify({ type: 'run.outcome_seen', run_id: 'r24', payload: {} }) })
    release()
    await pending
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(shown.length, 1, 'only the run.status need showed')
  } finally {
    globalThis.fetch = fetched
    mock.restoreAll()
  }
})
