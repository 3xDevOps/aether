'use strict'

const fs = require('node:fs')
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

// What app.relaunch gets for target: null to relaunch this shell as it was
// started, which reuses its whole command line, or the other app's
// executable with this shell's arguments. Electron drops the original
// command line once execPath is given (App::Relaunch), and on linux that
// line carries the launcher's --no-sandbox. A target that does not resolve
// falls back to this shell rather than relaunching nothing.
function relaunchOptions(target, execPath, argv, platform = process.platform, realpath = fs.realpathSync) {
  if (!target) return null
  let resolved, own
  try {
    resolved = realpath(target)
    own = realpath(execPath)
  } catch {
    return null
  }
  const foldCase = platform === 'darwin' || platform === 'win32'
  if (foldCase ? resolved.toLowerCase() === own.toLowerCase() : resolved === own) return null
  return { execPath: target, args: argv.slice(1) }
}

module.exports = { relaunchOptions, relaunchTarget, sidecarExitAction }
