// Minimal, deliberately boring preload: the SPA gets to know it is inside the
// desktop shell, on which platform, and how to drive its own title bar's
// window buttons. No raw ipcRenderer and no Node reaches the page - it stays a
// plain browser client of the tokened gateway.

'use strict'

const { contextBridge, ipcRenderer } = require('electron')

// Absent on darwin: the native traffic lights are kept there, and their
// absence is exactly how the SPA knows to reserve room for them instead of
// drawing buttons of its own.
const controls =
  process.platform === 'darwin'
    ? undefined
    : {
        minimize: () => ipcRenderer.send('window:minimize'),
        toggleMaximize: () => ipcRenderer.send('window:toggle-maximize'),
        close: () => ipcRenderer.send('window:close'),
        isMaximized: () => ipcRenderer.invoke('window:is-maximized'),
        onMaximizedChange: (cb) => {
          const listener = (_event, maximized) => cb(maximized)
          ipcRenderer.on('window:maximized-change', listener)
          // Returned so a React effect can unsubscribe on unmount.
          return () => ipcRenderer.removeListener('window:maximized-change', listener)
        },
      }

contextBridge.exposeInMainWorld('aetherDesktop', {
  platform: process.platform,
  ...(controls ? { controls } : {}),
})
