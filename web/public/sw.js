// Shows push notifications and opens the run one is about. There is no fetch
// handler: the dashboard is never served or cached from here.

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
  event.waitUntil(
    self.registration.showNotification(message.title || 'Aether', {
      body: message.body || 'A run needs you.',
      // One notification per run: a newer one replaces the older and alerts
      // again.
      tag: message.run || 'aether',
      renotify: true,
      icon: '/icons/icon-192.png',
      data: { run: message.run },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const run = event.notification.data?.run
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      const open = windows.find((client) => client.focused) ?? windows[0]
      if (!open) {
        await self.clients.openWindow(run ? `/?run=${encodeURIComponent(run)}` : '/')
        return
      }
      if (run) open.postMessage({ type: 'aether:open-run', run })
      await open.focus()
    })(),
  )
})
