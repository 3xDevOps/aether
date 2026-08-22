// Minimal, deliberately boring preload: the SPA gets to know it is inside
// the desktop shell and on which platform, and nothing else. No IPC, no
// Node - the page stays a plain browser client of the tokened gateway.

'use strict'

const { contextBridge } = require('electron')

contextBridge.exposeInMainWorld('aetherDesktop', {
  platform: process.platform,
})
