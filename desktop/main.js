// Aether desktop shell: a thin Electron window around `aether gui`.
//
// The CLI sidecar owns all authority - the SSH identity, the repo, the
// per-process gateway token. This process only spawns it, reads the one
// JSON line it prints ({"url": ..., "addr": ...}), and points a sandboxed
// BrowserWindow at the tokened loopback URL. The aether binary is NOT
// bundled; it is discovered via AETHER_BIN, PATH, or the install script's
// default locations.

'use strict'

const { app, BrowserWindow, dialog, ipcMain, Menu, shell } = require('electron')
const { spawn } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')
const readline = require('node:readline')

const notify = require('./notify')

// The CLI version that built this shell, stamped into package.json by
// `aether gui build`. It rides down to the renderer as an argv entry the
// preload reads, so the dashboard can tell a shell built by an older CLI
// from one built by the CLI now serving the gateway.
const shellVersion = require('./package.json').version

// The exit status `aether gui --json` uses to say it replaced this app on
// disk: relaunch the shell, do not respawn the sidecar (localgw.ExitRelaunch).
const RELAUNCH_EXIT = 75

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
      // The gateway rebuilt this app on disk (`update.apply`) and asked for
      // a relaunch: respawning the sidecar would leave the user in the old
      // shell around a new CLI. Everything else keeps the respawn path.
      if (code === RELAUNCH_EXIT) {
        quitting = true
        app.relaunch()
        app.exit(0)
        return
      }
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

  // --- window chrome ---------------------------------------------------------

  // The SPA draws its own title bar, so the shell's generic File/Edit/View
  // menu is dead weight - on win32/linux it is drawn *inside* the frameless
  // window and would sit on top of that bar. darwin is different: its menu
  // lives in the system menu bar, not the window, and nulling it strips the
  // renderer of Cmd+C/V/A and Cmd+Q. So darwin keeps a roles-only menu.
  function installMenu() {
    if (process.platform !== 'darwin') {
      Menu.setApplicationMenu(null)
      return
    }
    Menu.setApplicationMenu(
      Menu.buildFromTemplate([{ role: 'appMenu' }, { role: 'editMenu' }, { role: 'windowMenu' }]),
    )
  }

  // Window controls for the SPA's title bar. The window is resolved from the
  // sender, never from the `win` closure variable: a stale reference must not
  // be able to drive a different window than the one that asked.
  function senderWindow(event) {
    const target = BrowserWindow.fromWebContents(event.sender)
    return target && !target.isDestroyed() ? target : null
  }

  ipcMain.on('window:minimize', (event) => {
    senderWindow(event)?.minimize()
  })
  ipcMain.on('window:toggle-maximize', (event) => {
    const target = senderWindow(event)
    if (!target) return
    if (target.isMaximized()) target.unmaximize()
    else target.maximize()
  })
  ipcMain.on('window:close', (event) => {
    senderWindow(event)?.close()
  })
  ipcMain.handle('window:is-maximized', (event) => senderWindow(event)?.isMaximized() ?? false)

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
      // Frameless: the SPA draws a 36px title bar itself. On darwin the frame
      // stays (frame:false there would delete the traffic lights too); hiding
      // just the title bar keeps them, and y:11 centers the 12px lights in
      // that 36px bar.
      ...(process.platform === 'darwin'
        ? { titleBarStyle: 'hidden', trafficLightPosition: { x: 12, y: 11 } }
        : { frame: false }),
      // Matches the dashboard's --background token, so a frameless window
      // does not flash white before the SPA paints.
      backgroundColor: '#05070f',
      webPreferences: {
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        backgroundThrottling: false,
        preload: path.join(__dirname, 'preload.js'),
        additionalArguments: ['--aether-shell-version=' + shellVersion],
      },
    })

    // The SPA's maximize/restore button needs to track state changes it did
    // not cause (double-click on the bar, window manager shortcuts). Bound to
    // this window, not the `win` closure, so a later window cannot be driven
    // by an older window's events.
    const created = win
    const sendMaximized = (maximized) => () => {
      if (created.isDestroyed()) return
      created.webContents.send('window:maximized-change', maximized)
    }
    created.on('maximize', sendMaximized(true))
    created.on('unmaximize', sendMaximized(false))

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

  app.whenReady().then(() => {
    installMenu()
    // Windows/Linux hand a cold-start aether:// link to the first
    // instance's argv; handleDeepLink queues it until the gateway is up.
    const link = process.argv.find((arg) => arg.startsWith('aether://'))
    if (link) handleDeepLink(link)
    startSidecar()
  })

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
