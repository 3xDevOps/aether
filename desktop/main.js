// Cloudfield desktop shell: a thin Electron window around `aether gui`.
//
// The CLI sidecar owns all authority - the SSH identity, the repo, the
// per-process gateway token. This process only spawns it, reads the one
// JSON line it prints ({"url": ..., "addr": ...}), and points a sandboxed
// BrowserWindow at the tokened loopback URL. The aether binary is NOT
// bundled; it is discovered via AETHER_BIN, the CLI that installed this app,
// PATH, or the install script's default locations.

'use strict'

const { app, BrowserWindow, dialog, ipcMain, Menu, shell } = require('electron')
const { spawn } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')
const readline = require('node:readline')

const { boundCli } = require('./cli-binding')
const { SCHEMES, deepLinkSuffix, findDeepLink } = require('./deep-link')
const notify = require('./notify')
const { relaunchOptions, relaunchTarget, sidecarExitAction } = require('./sidecar-exit')
const { chooseUserData } = require('./user-data')
const zoom = require('./zoom')

// The full CLI build identity is separate from package.json's npm-valid
// version: git-describe and dev builds cannot use their identities there.
// Older shells only carried the package version.
const manifest = require('./package.json')
const shellVersion = manifest.aetherCliVersion || manifest.version
// CLOUDFIELD_BIN when set, else AETHER_BIN (internal/envname).
const explicitBin = process.env.CLOUDFIELD_BIN || process.env.AETHER_BIN
// A damaged binding file is reported through fatal() once the app is ready,
// not as Electron's uncaught-exception dialog at load.
let installedBy = ''
let bindingError = null
try {
  installedBy = boundCli(process.resourcesPath, manifest)
} catch (err) {
  bindingError = err
}

// --- single instance ----------------------------------------------------

// Before the lock, which lives in the data directory. setPath refuses a
// directory that does not exist, so it only runs to pick the earlier one.
const appData = app.getPath('appData')
const userData = chooseUserData(appData, app.name)
if (userData !== path.join(appData, app.name)) app.setPath('userData', userData)

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
  // What a deep link appends to the gateway URL, held until the gateway is
  // up: the query &run=<id>, or the fragment #join=<link> or #claim=<link>,
  // which keeps the link's secret out of the request to the gateway.
  let pendingSuffix = ''

  // --- binary discovery ---------------------------------------------------

  // The application menu may still have the PATH from before installation.
  function installLocations() {
    if (process.platform === 'win32') {
      const local = process.env.LOCALAPPDATA
      return local && path.isAbsolute(local)
        ? [path.join(local, 'Programs', 'Aether', 'aether.exe')]
        : []
    }
    const home = app.getPath('home')
    return ['/usr/local/bin/aether', path.join(home, '.local', 'bin', 'aether')]
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
    if (explicitBin) {
      try {
        fs.accessSync(explicitBin, fs.constants.X_OK)
        return explicitBin
      } catch {
        return '' // an explicit path that does not work is an error, not a fallback
      }
    }
    // A freshly installed shell must launch the executable that installed
    // it, not an older copy earlier on the desktop PATH.
    // Keep AETHER_BIN as an intentional override; an unavailable pinned
    // binary is an error rather than permission to silently run another.
    if (installedBy) {
      try {
        fs.accessSync(installedBy, fs.constants.X_OK)
        if (fs.statSync(installedBy).isFile()) return installedBy
      } catch {
        // The pinned binary no longer exists or cannot be run.
      }
      return ''
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
    if (bindingError) {
      fatal(
        'aether CLI binding unreadable',
        `${bindingError.message}\n\nRun aether gui build again to reinstall the app.`,
      )
      return
    }
    const bin = locateAether()
    if (!bin) {
      fatal(
        'aether CLI not found',
        installedBy && !explicitBin
          ? `The CLI that installed this app is no longer executable at ${installedBy}.\n\n` +
            'Restore it or set AETHER_BIN to the intended binary.'
          : 'The desktop app needs the aether CLI installed.\n\n' +
            'Install it (see docs/install.md), or set AETHER_BIN to the binary.',
      )
      return
    }

    child = spawn(bin, ['gui', '--json', '--port', '0'], {
      stdio: ['ignore', 'pipe', 'inherit'],
      windowsHide: true,
      // Tells the gateway this shell quits on exit 76 (localgw.ShellQuitsEnv)
      // and which app is running (localops.ShellExeEnv).
      env: { ...process.env, AETHER_SHELL_QUITS_ON_76: '1', AETHER_SHELL_EXE: process.execPath },
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
    let relaunchPath = ''
    let stdoutClosed = false
    rl.on('close', () => {
      stdoutClosed = true
    })
    rl.on('line', (line) => {
      if (parsed) {
        relaunchPath = relaunchTarget(line) || relaunchPath
        return
      }
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
      notify.start(msg.addr, msg.url, showRun, showLink)
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
      // The gateway replaced this app on disk (`update.apply`). Respawning
      // the sidecar would leave the user in the old shell around a new CLI;
      // on Windows a detached helper swaps the app directory once this
      // process is gone, so relaunching here would start into a half-swapped
      // one. Everything else keeps the respawn path.
      const action = sidecarExitAction(code)
      if (action === 'quit') {
        quitting = true
        app.exit(0)
        return
      }
      if (action === 'relaunch') {
        quitting = true
        // The line naming the app to relaunch can still be in the pipe
        // when the exit event fires; a pipe that never closes must not keep
        // the shell from relaunching.
        let relaunched = false
        const relaunch = () => {
          if (relaunched) return
          relaunched = true
          const options = relaunchOptions(relaunchPath, process.execPath, process.argv)
          if (options) app.relaunch(options)
          else app.relaunch()
          app.exit(0)
        }
        if (stdoutClosed) relaunch()
        else {
          rl.once('close', relaunch)
          setTimeout(relaunch, 2000)
        }
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
  // renderer of Cmd+C/V/A and Cmd+Q. So darwin keeps a roles-only menu. The
  // roles' default labels use app.name, which is package.json's name
  // (aether-desktop): it names the data directory, so it cannot change.
  function installMenu() {
    if (process.platform !== 'darwin') {
      Menu.setApplicationMenu(null)
      return
    }
    Menu.setApplicationMenu(
      Menu.buildFromTemplate([
        {
          role: 'appMenu',
          submenu: [
            { role: 'about', label: 'About Cloudfield' },
            { type: 'separator' },
            { label: 'Check for Updates…', click: checkForUpdates },
            { type: 'separator' },
            { role: 'services' },
            { type: 'separator' },
            { role: 'hide', label: 'Hide Cloudfield' },
            { role: 'hideOthers' },
            { role: 'unhide' },
            { type: 'separator' },
            { role: 'quit', label: 'Quit Cloudfield' },
          ],
        },
        { role: 'editMenu' },
        { role: 'windowMenu' },
      ]),
    )
  }

  // The SPA owns the update check and its result; the shell only asks it.
  function checkForUpdates() {
    focusWindow()
    if (win && !win.isDestroyed()) win.webContents.send('updates:check')
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

  // win32/linux have no application menu, so a right-click on the SPA's
  // title bar opens this one in its place.
  function popupWindowMenu(target, x, y) {
    Menu.buildFromTemplate([
      { label: 'Check for Updates…', click: () => !target.isDestroyed() && target.webContents.send('updates:check') },
      { type: 'separator' },
      { label: 'Minimize', click: () => target.minimize() },
      target.isMaximized()
        ? { label: 'Restore', click: () => target.unmaximize() }
        : { label: 'Maximize', click: () => target.maximize() },
      { type: 'separator' },
      { label: 'Close', click: () => target.close() },
    ]).popup({ window: target, x: Math.round(x), y: Math.round(y) })
  }

  ipcMain.on('window:menu', (event, point) => {
    const target = senderWindow(event)
    if (!target || !Number.isFinite(point?.x) || !Number.isFinite(point?.y)) return
    // The page reports CSS pixels; the menu is placed in window pixels.
    const factor = target.webContents.getZoomFactor()
    popupWindowMenu(target, point.x * factor, point.y * factor)
  })
  ipcMain.on('window:zoom', (event, step) => {
    const target = senderWindow(event)
    if (target && (step === 'in' || step === 'out' || step === 'reset')) zoom.zoomWindow(target, step)
  })

  ipcMain.on('updates:notify', (_event, update) => {
    const { key, title, body } = update || {}
    if (typeof key !== 'string' || !/^(server|cli):\S{1,64}$/.test(key)) return
    if (typeof title !== 'string' || typeof body !== 'string') return
    notify.showUpdate({ key, title, body }, checkForUpdates)
  })

  // KDE's portal backend shows nothing for the x11: parent handle Chromium sends
  // when Electron draws on X11 under Wayland, which it does unless the user sets
  // an ozone platform, so KDE under Wayland asks without a parent. Chromium
  // reports a failed portal as a cancel and a hung one never settles, so window
  // focus is the only sign a chooser opened.
  const chooserTail =
    'Electron asks xdg-desktop-portal for the chooser when the portal offers one (version 4 or newer), kdialog on KDE when installed, and GTK otherwise; run the app from a terminal to see any error Electron prints'

  ipcMain.handle('dialog:choose-folder', async (event) => {
    const target = senderWindow(event)
    if (!target) throw new Error('no window asked for the folder dialog')
    const options = { title: 'Choose repository folder', properties: ['openDirectory'] }
    if (process.platform !== 'linux') {
      const { canceled, filePaths } = await dialog.showOpenDialog(target, options)
      return canceled ? '' : filePaths[0]
    }
    const kdeWayland =
      process.env.WAYLAND_DISPLAY &&
      (process.env.XDG_CURRENT_DESKTOP ?? '').split(':').includes('KDE')
    const { canceled, filePaths } = await new Promise((resolve, reject) => {
      const focused = target.isFocused()
      let blurred = !focused
      let timedOut = false
      let timer
      const done = () => {
        clearTimeout(timer)
        target.removeListener('blur', onBlur)
        target.removeListener('focus', onFocus)
      }
      const arm = () => {
        timer = setTimeout(() => {
          done()
          timedOut = true
          reject(new Error('the window held focus for the last 10s, so no chooser seems to have opened. ' + chooserTail))
        }, 10_000)
      }
      const onBlur = () => {
        blurred = true
        clearTimeout(timer)
      }
      const onFocus = () => {
        clearTimeout(timer)
        arm()
      }
      target.on('blur', onBlur)
      target.on('focus', onFocus)
      if (focused) arm()
      const shown = kdeWayland
        ? dialog.showOpenDialog(options)
        : dialog.showOpenDialog(target, options)
      shown.then(
        (result) => {
          done()
          if (timedOut) {
            if (!result.canceled && !target.isDestroyed()) {
              target.webContents.send('dialog:folder-chosen', result.filePaths[0])
            }
          } else if (result.canceled && !blurred) {
            reject(new Error('the folder chooser closed without opening: the window kept focus throughout. ' + chooserTail))
          } else {
            resolve(result)
          }
        },
        (err) => {
          done()
          if (!timedOut) reject(err)
        },
      )
    })
    return canceled ? '' : filePaths[0]
  })

  // --- window ---------------------------------------------------------------

  function openWindow() {
    const url = gatewayURL + pendingSuffix
    pendingSuffix = ''
    if (win && !win.isDestroyed()) {
      win.loadURL(url)
      focusWindow()
      return
    }
    win = new BrowserWindow({
      // Shown until the page sets its own, and on Windows and Linux would
      // otherwise be app.name (aether-desktop).
      title: 'Cloudfield',
      width: 1280,
      height: 840,
      minWidth: zoom.MIN_WIDTH,
      minHeight: zoom.MIN_HEIGHT,
      // Keep native macOS traffic lights centered in the 44px navigation row.
      ...(process.platform === 'darwin'
        ? { titleBarStyle: 'hidden', trafficLightPosition: zoom.TRAFFIC_LIGHTS }
        : { frame: false }),
      // The dashboard's default dark --canvas, so a frameless window does not
      // flash another colour before the SPA paints.
      backgroundColor: '#070b11',
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
    // A right-click on a drag region never reaches the page on Windows;
    // the OS reports it here instead, in window coordinates.
    if (process.platform !== 'darwin') {
      created.on('system-context-menu', (event, point) => {
        event.preventDefault()
        popupWindowMenu(created, point.x, point.y)
      })
    }

    // The window is a browser locked to the gateway origin: anything else
    // opens in the user's real browser, never in this privileged shell.
    // The one other thing let out is the run view's Open in editor link, in
    // exactly the shape the dashboard writes it: a link in agent output
    // reaches this function too, and an editor scheme can carry far more
    // than "open this run's checkout".
    const editorLink = /^(?:(?:vscode|cursor):\/\/vscode-remote\/ssh-remote\+|zed:\/\/ssh\/)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.aether\/workspace$/
    const openExternalURL = (target) => {
      try {
        const protocol = new URL(target).protocol
        if (protocol === 'http:' || protocol === 'https:' || editorLink.test(target)) shell.openExternal(target)
      } catch {
        // Invalid navigation targets are denied without launching anything.
      }
    }
    win.webContents.setWindowOpenHandler(({ url: target }) => {
      openExternalURL(target)
      return { action: 'deny' }
    })
    win.webContents.on('will-navigate', (event, target) => {
      let sameGateway = false
      try {
        sameGateway = new URL(target).origin === gatewayOrigin
      } catch {
        // Invalid navigation targets are denied without launching anything.
      }
      if (!sameGateway) {
        event.preventDefault()
        openExternalURL(target)
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

  // <scheme>://run/<id> focuses the window on that run: the SPA reads
  // &run=<id> off the gateway URL and opens that run's terminal once it has
  // hydrated (docs/local-gateway.md). <scheme>://join?… and
  // <scheme>://claim?… open the Connect step with that link as #join= or
  // #claim=.
  function handleDeepLink(raw) {
    const suffix = deepLinkSuffix(raw)
    if (!suffix) return
    if (!gatewayURL) {
      pendingSuffix = suffix
      return
    }
    if (win && !win.isDestroyed()) {
      win.loadURL(gatewayURL + suffix)
      focusWindow()
    } else {
      pendingSuffix = suffix
      openWindow()
    }
  }

  // Opens a run in the page as it stands. The dashboard keeps its route in
  // the address and follows popstate, and a reload would discard a message
  // the user has typed and not sent. A page that is not the dashboard, such
  // as a load that failed, is loaded at the run instead.
  function showRun(id) {
    showQuery(new URLSearchParams({ run: id }).toString())
  }

  function showQuery(query) {
    if (!win || win.isDestroyed()) {
      pendingSuffix = '&' + query
      openWindow()
      return
    }
    const address = JSON.stringify('?' + query)
    win.webContents
      .executeJavaScript(`history.pushState(null, '', ${address}); dispatchEvent(new PopStateEvent('popstate'))`)
      .catch(() => {
        if (win && !win.isDestroyed()) win.loadURL(gatewayURL + '&' + query)
      })
    focusWindow()
  }

  // A watch notice links to a dashboard page (/?...) or to a pull request on GitHub.
  function showLink(link) {
    if (link.startsWith('/?')) showQuery(new URLSearchParams(link.slice(2)).toString())
    else if (/^https:\/\//.test(link)) shell.openExternal(link)
  }

  for (const scheme of SCHEMES) app.setAsDefaultProtocolClient(scheme)

  // Windows/Linux deliver deep links through the second instance's argv.
  app.on('second-instance', (_event, argv) => {
    const link = findDeepLink(argv)
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
    // Windows/Linux hand a cold-start link to the first instance's argv;
    // handleDeepLink queues it until the gateway is up.
    const link = findDeepLink(process.argv)
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
