// Desktop notifications for needs-attention runs, fed by the gateway's
// /ws/events stream. Uses the WHATWG WebSocket that newer Electron main
// processes expose; where it is absent we skip notifications rather than
// pull in a dependency - the window itself still shows everything.

'use strict'

const { app, Notification } = require('electron')

// Runs currently needing attention; its size is the dock/taskbar badge.
const needsAttention = new Set()

let socket = null
let stopped = false
let attempt = 0
let reconnectTimer = null
let focusWindow = () => {}

/**
 * Start streaming events from the gateway.
 * @param {string} addr    host:port of the loopback gateway
 * @param {string} url     the full gateway URL; its ?token= query is the auth
 * @param {() => void} onActivate  focus the window when a notification is clicked
 */
function start(addr, url, onActivate) {
  stop() // a sidecar respawn hands us a fresh addr and token
  stopped = false
  attempt = 0
  focusWindow = onActivate

  if (typeof globalThis.WebSocket !== 'function') {
    // Older Electron main processes have no WHATWG WebSocket, and adding an
    // npm dependency just for notifications is not worth it.
    console.log('notify: no WebSocket in this Electron; desktop notifications disabled')
    return
  }

  const token = new URL(url).searchParams.get('token') || ''
  const scheme = url.startsWith('https:') ? 'wss' : 'ws'
  connect(`${scheme}://${addr}/ws/events?token=${token}`)
}

function connect(wsURL) {
  if (stopped) return
  const ws = new globalThis.WebSocket(wsURL)
  socket = ws

  ws.onopen = () => {
    attempt = 0
    // Live tail only: the SPA owns replay, we only care about transitions
    // that happen while the app is open.
    ws.send(JSON.stringify({ replay: false }))
  }

  ws.onmessage = (msg) => {
    let ev
    try {
      ev = JSON.parse(msg.data)
    } catch {
      return
    }
    if (ev.type !== 'run.status' || !ev.run_id) return
    const p = ev.payload || {}
    if (p.to === 'needs-attention') {
      if (!needsAttention.has(ev.run_id)) {
        needsAttention.add(ev.run_id)
        updateBadge()
        show(ev.run_id, p)
      }
    } else if (needsAttention.delete(ev.run_id)) {
      updateBadge()
    }
  }

  ws.onclose = () => {
    if (socket === ws) socket = null
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

function show(runId, payload) {
  if (!Notification.isSupported()) return
  const task = typeof payload.task === 'string' ? payload.task : ''
  const n = new Notification({
    title: task ? task.slice(0, 60) : runId,
    body: payload.reason || 'needs attention',
  })
  n.on('click', () => focusWindow())
  n.show()
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
