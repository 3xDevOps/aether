'use strict'

const fs = require('node:fs')
const path = require('node:path')

// The CLI this app is bound to. `aether gui build` writes
// resources/aether-cli.json beside the app when it installs it, so a
// prebuilt app carries no machine path of its own. Apps built before that
// file existed stamped the path into package.json instead. Empty when
// neither names one.
function boundCli(resourcesPath, manifest) {
  const file = path.join(resourcesPath, 'aether-cli.json')
  let raw
  try {
    raw = fs.readFileSync(file, 'utf8')
  } catch (err) {
    if (err.code !== 'ENOENT') throw err
    return manifest.aetherCliPath || ''
  }
  let bound
  try {
    bound = JSON.parse(raw).path
  } catch (err) {
    throw new Error(`${file}: ${err.message}`)
  }
  if (typeof bound !== 'string' || !path.isAbsolute(bound)) {
    throw new Error(`${file}: "path" must be an absolute path`)
  }
  return bound
}

module.exports = { boundCli }
