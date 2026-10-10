// Desktop notifications for needs-attention runs, fed by the gateway's
// /ws/events stream. Uses the WHATWG WebSocket that newer Electron main
// processes expose; where it is absent we skip notifications rather than
// pull in a dependency - the window itself still shows everything.

'use strict'

const { app, Notification } = require('electron')

// Runs currently needing attention; its size is the dock/taskbar badge.
const needsAttention = new Map()

let socket = null
let stopped = false
let attempt = 0
let reconnectTimer = null
let gateway = { origin: '', token: '' }
let openRun = () => {}

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
    if (ev.type !== 'run.status' || !ev.run_id) return
    const p = ev.payload || {}
    if (p.to === 'needs-attention') {
      if (!needsAttention.has(ev.run_id)) {
        const shown = { notification: null }
        needsAttention.set(ev.run_id, shown)
        updateBadge()
        void show(ev.run_id, p.reason, shown)
      }
    } else if (needsAttention.has(ev.run_id)) {
      needsAttention.get(ev.run_id).notification?.close()
      needsAttention.delete(ev.run_id)
      updateBadge()
    }
  }

  ws.onclose = () => {
    if (socket === ws) socket = null
    // With replay:false the set only reflects what this socket observed; a
    // gap makes it a lie, and the SPA is the source of truth for standing
    // attention. Drop everything and start clean on the next connection.
    if (needsAttention.size > 0) {
      needsAttention.clear()
      updateBadge()
    }
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

async function show(runId, reason, shown) {
  if (!Notification.isSupported()) return
  // run.status carries {from, to, reason} and no name, so the run is read
  // for it. A run that cannot be read is still announced, by its ID.
  let run = null
  try {
    const res = await fetch(`${gateway.origin}/api/v1/run.get`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${gateway.token}` },
      body: JSON.stringify({ run_id: runId }),
    })
    if (res.ok) run = (await res.json()).run
  } catch {
    // the gateway went away; the fallback below still names the run
  }
  // The run resumed, or the stream dropped, while its name was being read.
  // Needing attention again since then is another entry, with its own read.
  if (needsAttention.get(runId) !== shown) return
  shown.notification = new Notification({ title: run ? titleOf(run) : runId, body: bodyOf(run || { reason }) })
  shown.notification.on('click', () => openRun(runId))
  shown.notification.show()
}

// titleOf and bodyOf are runTitle and statusBody of internal/push/need.go,
// so this notification reads like the one a phone gets for the same run.

function titleOf(run) {
  const title = (run.title || '').trim()
  if (title) return clip(title)
  const line = (run.task || '').split('\n').map((l) => l.trim()).find(Boolean)
  return line ? clip(line) : 'Untitled run'
}

function clip(text) {
  const chars = Array.from(text)
  return chars.length <= 120 ? text : chars.slice(0, 120).join('').trimEnd() + '…'
}

const enhancedFailures = [
  'enhanced session failed: ',
  'enhanced session ended: ',
  'enhanced turn failed: ',
]

function bodyOf(run) {
  const reason = run.reason || ''
  if (reason.startsWith('blocked: ')) {
    return (run.mission_role === 'worker' ? 'Worker blocked: ' : 'Blocked: ') + reason.slice('blocked: '.length)
  }
  const failure = enhancedFailures.find((prefix) => reason.startsWith(prefix))
  if (failure) return 'Enhanced unavailable: ' + reason.slice(failure.length)
  if (reason === 'agent reported success' || reason === 'agent reported failure') {
    return 'Agent reported ' + reason.slice('agent reported '.length) + ', review the result'
  }
  if (reason.startsWith('stalled:')) return 'No activity'
  return run.acp ? 'Waiting for your reply' : 'Agent idle'
}

function updateBadge() {
  // No-op where the platform has no badge (Windows, some Linux DEs).
  try {
    app.setBadgeCount(needsAttention.size)
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
  needsAttention.clear()
  updateBadge()
}

module.exports = { start, stop }
