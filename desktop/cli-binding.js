'use strict'

const fs = require('node:fs')
const path = require('node:path')

// The CLI this app is bound to. `aether gui build` writes
// resources/aether-cli.json beside the app when it installs it, so a
// prebuilt app carries no machine path of its own. Apps built before that
// file existed stamped the path into package.json instead. Empty when
// neither names one.
function boundCli(resourcesPath, manifest) {
  let raw
  try {
    raw = fs.readFileSync(path.join(resourcesPath, 'aether-cli.json'), 'utf8')
  } catch (err) {
    if (err.code !== 'ENOENT') throw err
    return manifest.aetherCliPath || ''
  }
  const bound = JSON.parse(raw).path
  if (typeof bound !== 'string' || !path.isAbsolute(bound)) {
    throw new Error(`${path.join(resourcesPath, 'aether-cli.json')}: "path" must be an absolute path`)
  }
  return bound
}

module.exports = { boundCli }
