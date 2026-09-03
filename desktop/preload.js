// Minimal, deliberately boring preload: the SPA gets to know it is inside the
// desktop shell, on which platform, and how to drive its own title bar's
// window buttons. No raw ipcRenderer and no Node reaches the page - it stays a
// plain browser client of the tokened gateway.

'use strict'

const { contextBridge, ipcRenderer } = require('electron')

// Which CLI built this shell. `aether gui build` stamps its own version into
// the shell's package.json and main.js appends it to the renderer's argv: a
// sandboxed preload cannot read a file, and this is the documented way to
// hand a small value down. The SPA compares it with the CLI serving the
// gateway and says to rerun `aether gui build` once the two have drifted.
const shellVersionFlag = '--aether-shell-version='
const shellVersion = (process.argv || [])
  .find((arg) => arg.startsWith(shellVersionFlag))
  ?.slice(shellVersionFlag.length)

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
  ...(shellVersion ? { shellVersion } : {}),
  ...(controls ? { controls } : {}),
})
