// Shows push notifications, quietly replaces one whose need ended, and opens
// the run, or the workspace budget, one is about. There is no fetch handler: the dashboard is never served or cached
// from here.

self.addEventListener('install', () => {
  // The dashboard changes with the server binary, and so must this.
  self.skipWaiting()
})

self.addEventListener('push', (event) => {
  let message = {}
  try {
    message = event.data.json()
  } catch {
    // Safari drops a subscription whose push shows nothing, so a message
    // this worker cannot read still shows the fallback below.
  }
  if (message.clear && message.run) {
    // The run's need ended. A push that shows nothing would cost the
    // subscription on Safari, so the notification is replaced quietly
    // instead of closed.
    event.waitUntil(
      (async () => {
        const [shown] = await self.registration.getNotifications({ tag: message.run })
        await self.registration.showNotification(shown?.title || message.title || 'Aether', {
          body: message.body,
          tag: message.run,
          silent: true,
          icon: '/icons/icon-192.png',
          data: { run: message.run },
        })
      })(),
    )
    return
  }
  event.waitUntil(
    self.registration.showNotification(message.title || 'Aether', {
      body: message.body || 'A run needs you.',
      // One notification per run or workspace budget: a newer one replaces
      // the older and alerts again.
      tag: message.run || (message.workspace ? `budget:${message.workspace}` : 'aether'),
      renotify: true,
      icon: '/icons/icon-192.png',
      data: { run: message.run, workspace: message.workspace },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const { run, workspace } = event.notification.data ?? {}
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      const open = windows.find((client) => client.focused) ?? windows[0]
      if (!open) {
        const path = run
          ? `/?run=${encodeURIComponent(run)}`
          : workspace ? `/?page=workspace&id=${encodeURIComponent(workspace)}` : '/'
        await self.clients.openWindow(path)
        return
      }
      if (run) open.postMessage({ type: 'aether:open-run', run })
      else if (workspace) open.postMessage({ type: 'aether:open-workspace', workspace })
      await open.focus()
    })(),
  )
})
