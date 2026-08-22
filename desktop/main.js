// Aether desktop shell: a thin Electron window around `aether gui`.
//
// The CLI sidecar owns all authority - the SSH identity, the repo, the
// per-process gateway token. This process only spawns it, reads the one
// JSON line it prints ({"url": ..., "addr": ...}), and points a sandboxed
// BrowserWindow at the tokened loopback URL. The aether binary is NOT
// bundled; it is discovered via AETHER_BIN, PATH, or the install script's
// default locations.

'use strict'

const { app, BrowserWindow, dialog, shell } = require('electron')
const { spawn } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')
const readline = require('node:readline')

const notify = require('./notify')

// --- single instance ----------------------------------------------------

const locked = app.requestSingleInstanceLock()
if (!locked) {
  app.quit()
} else {
  main()
}

function main() {
  /** @type {BrowserWindow | null} */
  let win = null
  /** @type {import('node:child_process').ChildProcess | null} */
  let child = null
  // The `aether gui --json` URL: http://<addr>/?token=<token>.
  let gatewayURL = ''
  let gatewayOrigin = ''
  let respawns = 0
  let quitting = false
  // A deep link that arrived before the gateway was up.
  let pendingRunId = ''

  // --- binary discovery ---------------------------------------------------

  // Where scripts/install.sh puts the CLI: /usr/local/bin by default,
  // ~/.local/bin when there is no sudo.
  function installLocations() {
    const home = app.getPath('home')
    const names = process.platform === 'win32' ? ['aether.exe'] : ['aether']
    const dirs = ['/usr/local/bin', path.join(home, '.local', 'bin')]
    const out = []
    for (const dir of dirs) for (const name of names) out.push(path.join(dir, name))
    return out
  }

  // A which-style PATH lookup; Electron main has no `which` to shell out to
  // portably, and PATH search is three lines anyway.
  function findOnPath(name) {
    const exts = process.platform === 'win32' ? ['.exe', '.cmd', '.bat', ''] : ['']
    for (const dir of (process.env.PATH || '').split(path.delimiter)) {
      if (!dir) continue
      for (const ext of exts) {
        const candidate = path.join(dir, name + ext)
        try {
          fs.accessSync(candidate, fs.constants.X_OK)
          if (fs.statSync(candidate).isFile()) return candidate
        } catch {
          // not here; keep looking
        }
      }
    }
    return ''
  }

  function locateAether() {
    const explicit = process.env.AETHER_BIN
    if (explicit) {
      try {
        fs.accessSync(explicit, fs.constants.X_OK)
        return explicit
      } catch {
        return '' // an explicit path that does not work is an error, not a fallback
      }
    }
    const onPath = findOnPath('aether')
    if (onPath) return onPath
    for (const candidate of installLocations()) {
      try {
        fs.accessSync(candidate, fs.constants.X_OK)
        return candidate
      } catch {
        // keep looking
      }
    }
    return ''
  }

  // --- sidecar lifecycle ----------------------------------------------------

  function fatal(title, detail) {
    dialog.showErrorBox(title, detail)
    app.quit()
  }

  function startSidecar() {
    const bin = locateAether()
    if (!bin) {
      fatal(
        'aether CLI not found',
        'The desktop app needs the aether CLI installed and linked.\n\n' +
          'Install it (see docs/install.md), or set AETHER_BIN to the binary.',
      )
      return
    }

    child = spawn(bin, ['gui', '--json', '--port', '0'], {
      stdio: ['ignore', 'pipe', 'inherit'],
    })

    // The sidecar prints exactly one JSON line on stdout, then serves.
    const rl = readline.createInterface({ input: child.stdout })
    let parsed = false

    // Bounded handshake wait: a gateway hung on startup (SSH prompt on
    // stderr, stdout silent) would otherwise leave a dock icon and no
    // window - the exit-driven respawn/fatal path never fires for a child
    // that neither talks nor dies. Kill it so that path takes over.
    const spawned = child
    let handshakeTimer = setTimeout(() => {
      handshakeTimer = null
      if (parsed || spawned !== child) return
      console.error('aether gui produced no handshake within 15s; killing it')
      spawned.kill('SIGKILL')
    }, 15000)
    rl.on('line', (line) => {
      if (parsed) return
      let msg
      try {
        msg = JSON.parse(line)
      } catch (err) {
        // Not the handshake line (a stray warning, say): keep waiting for it.
        console.error('aether gui printed an unparseable line:', line, err)
        return
      }
      if (handshakeTimer) {
        clearTimeout(handshakeTimer)
        handshakeTimer = null
      }
      parsed = true
      gatewayURL = msg.url
      gatewayOrigin = new URL(msg.url).origin
      respawns = 0 // a healthy start resets the backoff budget
      notify.start(msg.addr, msg.url, () => focusWindow())
      openWindow()
    })

    child.on('exit', (code, signal) => {
      if (handshakeTimer) {
        clearTimeout(handshakeTimer)
        handshakeTimer = null
      }
      child = null
      notify.stop()
      if (quitting) return
      respawns += 1
      if (respawns > 3) {
        fatal(
          'aether gui keeps exiting',
          `The gateway sidecar exited (code ${code}, signal ${signal}) ` +
            'and did not survive three restarts.\n\n' +
            'Run `aether gui` in a terminal to see why.',
        )
        return
      }
      // Bounded backoff: 1s, 2s, 4s.
      setTimeout(startSidecar, 1000 * 2 ** (respawns - 1))
    })
  }

  function stopSidecar() {
    if (!child) return
    const doomed = child
    child = null
    doomed.kill('SIGTERM')
    // SIGKILL if it lingers; unref so the timer never keeps us alive.
    const hardKill = setTimeout(() => doomed.kill('SIGKILL'), 5000)
    hardKill.unref()
    doomed.once('exit', () => clearTimeout(hardKill))
  }

  // --- window ---------------------------------------------------------------

  function openWindow() {
    const url = pendingRunId ? gatewayURL + '&run=' + pendingRunId : gatewayURL
    pendingRunId = ''
    if (win && !win.isDestroyed()) {
      win.loadURL(url)
      focusWindow()
      return
    }
    win = new BrowserWindow({
      width: 1280,
      height: 840,
      webPreferences: {
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        preload: path.join(__dirname, 'preload.js'),
      },
    })

    // The window is a browser locked to the gateway origin: anything else
    // opens in the user's real browser, never in this privileged shell.
    win.webContents.setWindowOpenHandler(({ url: target }) => {
      shell.openExternal(target)
      return { action: 'deny' }
    })
    win.webContents.on('will-navigate', (event, target) => {
      if (new URL(target).origin !== gatewayOrigin) {
        event.preventDefault()
        shell.openExternal(target)
      }
    })

    win.on('closed', () => {
      win = null
    })
    win.loadURL(url)
  }

  function focusWindow() {
    if (!win || win.isDestroyed()) return
    if (win.isMinimized()) win.restore()
    win.show()
    win.focus()
  }

  // --- deep links -------------------------------------------------------------

  // aether://run/<id> focuses the window on that run. Appending &run=<id> to
  // the gateway URL is harmless today: the SPA reads ?token= and ignores the
  // rest of the query.
  function handleDeepLink(raw) {
    let parsed
    try {
      parsed = new URL(raw)
    } catch {
      return
    }
    if (parsed.protocol !== 'aether:') return
    // aether://run/<id> parses as host "run", pathname "/<id>".
    if (parsed.hostname !== 'run') return
    const id = parsed.pathname.replace(/^\//, '')
    // Run IDs are lowercase ULIDs; refuse anything else rather than
    // concatenating attacker-shaped text into the gateway URL.
    if (!/^[0-9a-z]{10,32}$/.test(id)) return
    if (!gatewayURL) {
      pendingRunId = id
      return
    }
    if (win && !win.isDestroyed()) {
      win.loadURL(gatewayURL + '&run=' + id)
      focusWindow()
    } else {
      pendingRunId = id
      openWindow()
    }
  }

  app.setAsDefaultProtocolClient('aether')

  // Windows/Linux deliver deep links through the second instance's argv.
  app.on('second-instance', (_event, argv) => {
    const link = argv.find((arg) => arg.startsWith('aether://'))
    if (link) handleDeepLink(link)
    else focusWindow()
  })

  // macOS delivers them as open-url.
  app.on('open-url', (event, url) => {
    event.preventDefault()
    handleDeepLink(url)
  })

  // --- app lifecycle -------------------------------------------------------

  app.whenReady().then(startSidecar)

  app.on('activate', () => {
    if (!win && gatewayURL) openWindow()
  })

  app.on('window-all-closed', () => {
    // The sidecar holds SSH state; closing the window quits everywhere,
    // including macOS - a hidden app silently keeping an SSH tunnel open
    // is worse than re-launching.
    app.quit()
  })

  app.on('before-quit', () => {
    quitting = true
    notify.stop()
    stopSidecar()
  })
}
