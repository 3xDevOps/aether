'use strict'

const path = require('node:path')

// Exit statuses `aether gui --json` uses to say what the shell does next
// (localgw.ExitRelaunch, localgw.ExitUpdaterRestarts).
const RELAUNCH_EXIT = 75
const UPDATER_RESTARTS_EXIT = 76

// What the shell does when its sidecar exits on its own: 'relaunch' after
// update.apply replaced the app on disk, 'quit' on Windows where a detached
// helper swaps the app directory and starts the app again, and 'respawn'
// for every other exit.
function sidecarExitAction(code) {
  if (code === RELAUNCH_EXIT) return 'relaunch'
  if (code === UPDATER_RESTARTS_EXIT) return 'quit'
  return 'respawn'
}

// The executable a line from the sidecar names for the relaunch after exit
// 75, empty for any other line. It differs from this shell's own when the
// rebuild installed a renamed app beside it.
function relaunchTarget(line) {
  let msg
  try {
    msg = JSON.parse(line)
  } catch {
    return ''
  }
  const target = msg?.relaunch
  return typeof target === 'string' && path.isAbsolute(target) ? target : ''
}

module.exports = { relaunchTarget, sidecarExitAction }
