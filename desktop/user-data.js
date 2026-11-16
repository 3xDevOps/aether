'use strict'

const fs = require('node:fs')
const path = require('node:path')

// The data directory of an app whose package.json has the name
// aether-desktop and no productName: Electron names it after productName,
// else name (lib/browser/init.ts).
const LEGACY_USER_DATA = 'aether-desktop'

// The data directory for an app called name: its own when that exists, the
// one earlier apps used when only that exists, else its own. Electron's
// app.getPath('userData') creates the directory it returns, so the choice is
// made from appData and the name before anything asks Electron for it.
function chooseUserData(appData, name, exists = fs.existsSync) {
  const own = path.join(appData, name)
  const legacy = path.join(appData, LEGACY_USER_DATA)
  return own !== legacy && !exists(own) && exists(legacy) ? legacy : own
}

module.exports = { chooseUserData }
