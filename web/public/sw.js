// Shows push notifications, quietly replaces one whose need ended, and opens
// the page, run, or workspace budget one is about. There is no fetch
// handler: the dashboard is never served or cached from here.

self.addEventListener('install', () => {
  // The dashboard changes with the server binary, and so must this.
  self.skipWaiting()
})

// Pushes are shown in arrival order: a clear still reading the shown
// notification must not land over a newer need for the same run.
let shown = Promise.resolve()

self.addEventListener('push', (event) => {
  let message = {}
  try {
    message = event.data.json()
  } catch {
    // Safari drops a subscription whose push shows nothing, so a message
    // this worker cannot read still shows the fallback below.
  }
  const show = shown.then(() => display(message))
  shown = show.catch(() => {})
  event.waitUntil(show)
})

async function display(message) {
  const cleared = message.tag || message.run || message.url
  if (message.clear && cleared) {
    // The need of a watch, of the run, or of the page a need opens, ended. A push that shows nothing would cost the subscription on
    // Safari, so the notification is replaced quietly instead of closed.
    const [current] = await self.registration.getNotifications({ tag: cleared })
    // An assistant's newer notice about the run stays, shown again as it
    // was, as does the outcome this device decided a card with.
    const notice = current?.data?.notice === true || current?.data?.decided === true
    await self.registration.showNotification(current?.title || message.title || 'Aether', {
      body: notice ? current.body : message.body,
      tag: cleared,
      silent: true,
      icon: '/icons/icon-192.png',
      data: notice ? current.data : { run: message.run, url: message.url },
    })
    return
  }
  await self.registration.showNotification(message.title || 'Aether', {
    body: message.body || 'A session needs you.',
    // One notification per watch notice, run, workspace budget or page: a
    // newer one replaces the older and alerts again, but for a summary of
    // pushes that arrived within two minutes of it.
    tag: message.tag || message.run || (message.workspace ? `budget:${message.workspace}` : message.url || 'aether'),
    ...(message.quiet ? { silent: true } : { renotify: true }),
    icon: '/icons/icon-192.png',
    // An assistant card's push carries only its id. Approve opens the
    // card, since an action runs on a locked phone; Decline is decided from
    // here with this device's own sign-in. Safari shows no actions, so a tap
    // opens the card.
    ...(message.card ? { actions: [{ action: 'approve', title: 'Approve' }, { action: 'decline', title: 'Decline' }] } : {}),
    data: { run: message.run, workspace: message.workspace, notice: message.notice, url: message.url, card: message.card },
  })
}

// decline declines card through assistant.confirm, and puts the outcome,
// or the server's own error, in place of the notification.
async function decline(notification, card) {
  let body = 'Declined'
  try {
    const res = await fetch('/api/v1/assistant.confirm', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ confirmation_id: card, approve: false }),
    })
    if (!res.ok) {
      const refusal = await res.json().catch(() => null)
      body = `Not decided: ${refusal?.error?.message || `HTTP ${res.status}`}`
    }
  } catch (err) {
    body = `Not decided: ${err instanceof Error ? err.message : String(err)}`
  }
  await self.registration.showNotification(notification.title || 'Aether', {
    body,
    tag: notification.tag,
    silent: true,
    icon: '/icons/icon-192.png',
    data: { url: notification.data?.url, decided: true },
  })
}

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const { run, workspace, url, card } = event.notification.data ?? {}
  if (card && event.action === 'decline') {
    event.waitUntil(decline(event.notification, card))
    return
  }
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      const open = windows.find((client) => client.focused) ?? windows[0]
      if (!open) {
        const path = url
          || (run ? `/?run=${encodeURIComponent(run)}`
            : workspace ? `/?page=workspace&id=${encodeURIComponent(workspace)}` : '/')
        await self.clients.openWindow(path)
        return
      }
      // The page routes itself: reloading it would lose its unsent drafts.
      if (run) open.postMessage({ type: 'aether:open-run', run })
      else if (workspace) open.postMessage({ type: 'aether:open-workspace', workspace })
      await open.focus()
      if (url && !(await acknowledged(open, { type: 'aether:open-url', url }))) {
        await open.navigate(url).catch(() => self.clients.openWindow(url))
      }
    })(),
  )
})

// A dashboard built before aether:open-url ignores it, so one that does not
// answer within a second is sent to the page instead.
function acknowledged(client, message) {
  const channel = new MessageChannel()
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), 1000)
    channel.port1.onmessage = () => {
      clearTimeout(timer)
      resolve(true)
    }
    client.postMessage(message, [channel.port2])
  }).finally(() => channel.port1.close())
}
