// Desktop notifications for runs that need the member, fed by the gateway's
// /ws/events stream. Uses the WHATWG WebSocket that newer Electron main
// processes expose; where it is absent we skip notifications rather than
// pull in a dependency - the window itself still shows everything.

'use strict'

const { app, Notification } = require('electron')

// Runs that need the member now, with the need's key and notification; its
// size is the dock/taskbar badge.
const standing = new Map()
// The need key last announced per run. It outlives a reconnect, so a need
// already announced stays quiet when its run's next event arrives.
const announced = new Map()
// The highest status_seq seen per run; an event at or below it is stale.
const seqs = new Map()
// The run.get read in flight per run; a newer event supersedes it.
const reading = new Map()

let socket = null
let stopped = false
let attempt = 0
let reconnectTimer = null
let gateway = { origin: '', token: '' }
let openRun = () => {}
let selfID = null

// When each run last raised a needs-you notification, by either path. The
// same transition can reach us as run.status and as assistant.notice kind
// needs_attention; other notice kinds are different events.
const lastShown = new Map()
const duplicateWindowMs = 5000

function recentlyShown(runId) {
  return Date.now() - (lastShown.get(runId) || 0) < duplicateWindowMs
}

/**
 * Start streaming events from the gateway.
 * @param {string} addr    host:port of the loopback gateway
 * @param {string} url     the full gateway URL; its ?token= query is the auth
 * @param {(runId: string) => void} onOpen  open a run when its notification is clicked
 */
function start(addr, url, onOpen) {
  stop() // a sidecar respawn hands us a fresh addr and token
  stopped = false
  attempt = 0
  openRun = onOpen
  selfID = null
  lastShown.clear()
  announced.clear()
  seqs.clear()

  if (typeof globalThis.WebSocket !== 'function') {
    // Older Electron main processes have no WHATWG WebSocket, and adding an
    // npm dependency just for notifications is not worth it.
    console.log('notify: no WebSocket in this Electron; desktop notifications disabled')
    return
  }

  const parsed = new URL(url)
  gateway = { origin: parsed.origin, token: parsed.searchParams.get('token') || '' }
  const scheme = url.startsWith('https:') ? 'wss' : 'ws'
  connect(`${scheme}://${addr}/ws/events?token=${gateway.token}`)
}

function connect(wsURL) {
  if (stopped) return
  const ws = new globalThis.WebSocket(wsURL)
  socket = ws

  ws.onopen = () => {
    // Live tail only: the SPA owns replay, we only care about transitions
    // that happen while the app is open. Note: attempt is NOT reset here -
    // an open-then-immediate-close storm (e.g. a refused token) must keep
    // backing off; the reset waits for the subscribe ack.
    ws.send(JSON.stringify({ replay: false }))
  }

  ws.onmessage = (msg) => {
    let ev
    try {
      ev = JSON.parse(msg.data)
    } catch {
      return
    }
    if (ev.ok === true) {
      // Subscribe ack: the gateway accepted us, so the backoff can reset.
      attempt = 0
      return
    }
    if (ev.type === 'assistant.notice') {
      void showNotice(ev.payload || {})
      return
    }
    if (!ev.run_id) return
    if (ev.type === 'run.status') onStatus(ev.run_id, ev.payload || {})
    else if (ev.type === 'run.outcome_seen') onOutcomeSeen(ev.run_id)
  }

  ws.onclose = () => {
    if (socket === ws) socket = null
    // With replay:false the set only reflects what this socket observed; a
    // gap makes it a lie, and the SPA is the source of truth for standing
    // attention. Drop everything and start clean on the next connection.
    if (standing.size > 0) {
      standing.clear()
      updateBadge()
    }
    reading.clear()
    scheduleReconnect(wsURL)
  }
  ws.onerror = () => {
    // onclose follows and handles the reconnect.
  }
}

function scheduleReconnect(wsURL) {
  if (stopped || reconnectTimer) return
  // 1s, 2s, 4s, ... capped at 30s.
  const delay = Math.min(1000 * 2 ** attempt, 30000)
  attempt += 1
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null
    connect(wsURL)
  }, delay)
}

async function memberID() {
  if (selfID) return selfID
  const res = await fetch(`${gateway.origin}/api/v1/server.info`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${gateway.token}` },
    body: '{}',
  })
  if (!res.ok) throw new Error(`server.info: ${res.status}`)
  selfID = (await res.json()).member.id
  return selfID
}

// assistant.notice goes to every subscriber; only the member it names reacts.
async function showNotice(p) {
  if (!Notification.isSupported() || !p.run_id) return
  try {
    if (p.member_id !== (await memberID())) return
  } catch (err) {
    console.log(`notify: cannot tell who this member is: ${err.message}`)
    return
  }
  if (p.kind === 'needs_attention') {
    const duplicate = recentlyShown(p.run_id)
    lastShown.set(p.run_id, Date.now())
    if (duplicate) return
  }
  const notification = new Notification({ title: p.title || p.run_id, body: p.body || '' })
  notification.on('click', () => openRun(p.run_id))
  notification.show()
}

function onStatus(runId, p) {
  if (p.status_seq > 0 && p.status_seq <= (seqs.get(runId) || 0)) return
  if (p.status_seq > 0) seqs.set(runId, p.status_seq)
  const seen = { status: p.to, reason: p.reason || '', state: p.state || '', outcome_unseen: p.outcome_unseen === true }
  // The event carries neither paused nor mission_role, so it is first read
  // as a lead run, whose needs include a worker's, then checked on run.get.
  if (!bodyOf(seen)) {
    resolve(runId)
    return
  }
  const token = {}
  reading.set(runId, token)
  void Promise.all([readRun(runId), memberID().catch(() => null)]).then(([run, self]) => {
    if (reading.get(runId) !== token) return
    reading.delete(runId)
    // /ws/events carries every run in the workspace; like push, only the
    // run's owner hears about it. Without the run or the member there is no
    // owner to check, so nothing is announced.
    if (!run || !self) return
    if (run.member_id !== self) {
      resolve(runId)
      return
    }
    announce(runId, run)
  })
}

function onOutcomeSeen(runId) {
  const entry = standing.get(runId)
  if (entry && !needsAttention({ ...entry.run, outcome_unseen: false })) resolve(runId)
}

async function readRun(runId) {
  try {
    const res = await fetch(`${gateway.origin}/api/v1/run.get`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${gateway.token}` },
      body: JSON.stringify({ run_id: runId }),
    })
    if (res.ok) return (await res.json()).run
    console.log(`notify: run.get ${runId}: ${res.status}`)
  } catch (err) {
    console.log(`notify: run.get ${runId}: ${err.message}`)
  }
  return null
}

function announce(runId, run) {
  const body = run.paused ? '' : bodyOf(run)
  if (!body) {
    resolve(runId)
    return
  }
  const key = needKey(run)
  const attention = needsAttention(run)
  let entry = standing.get(runId)
  if (entry && (!attention || entry.key !== key)) {
    resolve(runId)
    entry = undefined
  }
  if (attention && !entry) {
    entry = { key, run, notification: null }
    standing.set(runId, entry)
    updateBadge()
  }
  if (announced.get(runId) === key) return
  announced.set(runId, key)
  if (attention) {
    const duplicate = recentlyShown(runId)
    lastShown.set(runId, Date.now())
    if (duplicate) return
  }
  if (!Notification.isSupported()) return
  const notification = new Notification({
    title: titleOf(run),
    body,
    ...(attention ? {} : { silent: true, urgency: 'low' }),
  })
  notification.on('click', () => openRun(runId))
  notification.show()
  if (entry) entry.notification = notification
}

function resolve(runId) {
  reading.delete(runId)
  const entry = standing.get(runId)
  if (!entry) return
  entry.notification?.close()
  standing.delete(runId)
  updateBadge()
}

// needsAttention and needKey are domain.Run.NeedsAttention and NeedKey in
// internal/domain/domain.go; titleOf and statusBody are RunTitle and
// statusBody of internal/push/need.go. A desktop notification fires and
// reads like the one a phone gets for the same run.

function needsAttention(run) {
  if (run.paused || (run.status !== 'running' && run.status !== 'needs-attention')) return false
  if (run.mission_role === 'worker') return run.state === 'needs-you' && (run.reason || '').startsWith('blocked: ')
  if (run.state === 'needs-you' || run.state === 'disconnected') return true
  return run.state === 'failed' && run.outcome_unseen === true
}

function needKey(run) {
  return [run.status, run.state || '', run.reason || '', run.status_changed_at || ''].join('|')
}

function titleOf(run) {
  const title = (run.title || '').trim()
  if (title) return clip(title, 120)
  const line = (run.task || '').split('\n').map((l) => l.trim()).find(Boolean)
  return line ? clip(line, 120) : 'Untitled run'
}

function clip(text, limit) {
  const chars = Array.from(text)
  return chars.length <= limit ? text : chars.slice(0, limit).join('').trimEnd() + '…'
}

// An empty body means the run gets no notification.
function bodyOf(run) {
  return clip(statusBody(run), 240)
}

const disconnects = [
  ['enhanced session failed: ', 'Enhanced could not start: '],
  ['enhanced session ended: ', 'Agent disconnected: '],
]

function statusBody(run) {
  const reason = run.reason || ''
  const worker = run.mission_role === 'worker'
  if (!needsAttention(run)) {
    if (worker || !run.outcome_unseen) return ''
    if (run.state === 'finished') return 'Agent reported ' + reason.replace(/^agent reported /, '') + ', review the result'
    if (run.status === 'failed') return 'Failed, review the result'
    if (run.status === 'completed') return 'Finished, review the result'
    return ''
  }
  if (run.state === 'needs-you') {
    if (reason.startsWith('blocked: ')) return (worker ? 'Worker blocked: ' : 'Blocked: ') + reason.slice('blocked: '.length)
    if (reason.startsWith('needs input: ')) return reason.slice('needs input: '.length)
    if (reason.startsWith('Usage limit reached on ')) return reason
    return 'Waiting for your reply'
  }
  if (run.state === 'disconnected') {
    const match = disconnects.find(([prefix]) => reason.startsWith(prefix))
    return match ? match[1] + reason.slice(match[0].length) : 'Agent disconnected'
  }
  if (reason.startsWith('enhanced turn failed: ')) return 'Turn failed: ' + reason.slice('enhanced turn failed: '.length)
  return reason === 'agent reported failure' ? 'Agent reported failure, review the result' : 'Agent failed'
}

function updateBadge() {
  // No-op where the platform has no badge (Windows, some Linux DEs).
  try {
    app.setBadgeCount(standing.size)
  } catch {
    // unsupported; ignore
  }
}

function stop() {
  stopped = true
  if (reconnectTimer) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
  if (socket) {
    try {
      socket.close()
    } catch {
      // already dead
    }
    socket = null
  }
  standing.clear()
  reading.clear()
  updateBadge()
}

module.exports = { start, stop }
