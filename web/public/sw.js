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
  const cleared = message.run || message.url
  if (message.clear && cleared) {
    // The need of the run, or of the page a swarm's need opens, ended. A
    // push that shows nothing would cost the subscription on Safari, so the
    // notification is replaced quietly instead of closed.
    const [current] = await self.registration.getNotifications({ tag: cleared })
    // An assistant's newer notice about the run stays, shown again as it was.
    const notice = current?.data?.notice === true
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
    body: message.body || 'A run needs you.',
    // One notification per run, workspace budget or page: a newer one
    // replaces the older and alerts again.
    tag: message.run || (message.workspace ? `budget:${message.workspace}` : message.url || 'aether'),
    renotify: true,
    icon: '/icons/icon-192.png',
    data: { run: message.run, workspace: message.workspace, notice: message.notice, url: message.url },
  })
}

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const { run, workspace, url } = event.notification.data ?? {}
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
      if (run) open.postMessage({ type: 'aether:open-run', run })
      else if (url) {
        await open.focus()
        // navigate() rejects for a window this worker does not control.
        await open.navigate(url).catch(() => self.clients.openWindow(url))
        return
      } else if (workspace) open.postMessage({ type: 'aether:open-workspace', workspace })
      await open.focus()
    })(),
  )
})
