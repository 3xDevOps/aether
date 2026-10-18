'use strict'

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

module.exports = { sidecarExitAction }
