// What a phone may and may not do to a run's terminal, against the real
// gateway: no socket in this file is answered by the test. The PTY is the
// per-dimension minimum over the clients that impose a geometry, so the
// claim under test is that a phone is not one of them - watching or
// steering - while still rendering what the desktop viewer sees.

import type { Member } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, shrinkToKeyboardHeight, test } from './mobile'

/** The desktop viewer's window, and so the session's geometry. */
const desktopCols = 132
const desktopRows = 43
const desktopSessionID = 'terminal-phone-desktop'
const probeSessionID = 'terminal-phone-probe'

interface AttachAck {
  ok: boolean
  cols: number
  rows: number
  error?: string
  control_generation?: number
}

/**
 * One attach straight to the member's gateway, the way the dashboard's own
 * socket does it: the tokened WebSocket, one header frame, one ack frame.
 * Resolves once the ack is in; the socket stays open until it is closed.
 */
async function attach(
  member: Member,
  runID: string,
  header: Record<string, unknown>,
): Promise<{ ack: AttachAck; sendInput: (data: string) => void; close: () => void }> {
  const url = new URL(member.url)
  const socket = new WebSocket(
    `ws://${url.host}/ws/attach/${runID}?token=${url.searchParams.get('token')}`,
  )
  const ack = await new Promise<AttachAck>((resolve, reject) => {
    const fail = (why: string) => reject(new Error(`attach ${runID}: ${why}`))
    socket.addEventListener('error', () => fail('socket error'))
    socket.addEventListener('close', (event) => fail(`closed: ${event.reason || event.code}`))
    socket.addEventListener('open', () => socket.send(JSON.stringify(header)))
    socket.addEventListener('message', (event) => {
      if (typeof event.data !== 'string') return
      resolve(JSON.parse(event.data) as AttachAck)
    })
  })
  return {
    ack,
    sendInput: (data) => socket.send(JSON.stringify({
      type: 'input',
      data,
      control_generation: ack.control_generation,
    })),
    close: () => socket.close(),
  }
}

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a phone follows a run terminal it cannot resize', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  // An agent that outlives the test: the run has to stay steerable, and the
  // seed repository's own script exits at once.
  aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'a run watched from a phone',
  })

  // A desktop viewer, steering at a desktop window's size. It is the only
  // client imposing one, so the session is that size.
  const desktop = await attach(alice, run.id, {
    write: true,
    cols: desktopCols,
    rows: desktopRows,
    control_session_id: desktopSessionID,
  })
  expect(desktop.ack.ok).toBe(true)
  // What the session is now, asked of the server rather than inferred: a
  // fresh follow attach is answered with the live PTY geometry.
  const sessionGeometry = async () => {
    const probe = await attach(alice, run.id, {
      follow: true,
      control_session_id: probeSessionID,
    })
    probe.close()
    return { cols: probe.ack.cols, rows: probe.ack.rows }
  }
  expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })

  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  await page
    .getByRole('dialog', { name: 'Runs' })
    .getByRole('button', { name: /watched from a phone/ })
    .tap()

  await expect(page.getByText('Attached')).toBeVisible()
  // Alice owns this run. On a desktop that attaches with write; on a phone
  // steering is a tap she has to make.
  await expect(page.getByRole('button', { name: 'Take control' })).toBeVisible()
  await expect(page.getByRole('toolbar', { name: 'Terminal keys' })).toBeHidden()

  // The phone draws the desktop viewer's grid: every row of it, at its
  // width, on a screen a third as wide, which is what it pans over.
  const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *) > div')
  await expect(rows).toHaveCount(desktopRows)
  const grid = () =>
    page.locator('.xterm:not([data-aether-frozen-view] *)').evaluate((el) => {
      const screen = el.querySelector('.xterm-screen') as HTMLElement | null
      const measure = el.querySelector('.xterm-char-measure-element') as HTMLElement | null
      const host = el.parentElement as HTMLElement
      // xterm measures its cell on 32 characters of the terminal's font.
      const cell = (measure?.getBoundingClientRect().width ?? 0) / 32
      return {
        cols: Math.round((screen?.getBoundingClientRect().width ?? 0) / cell),
        pannable: host.scrollWidth > host.clientWidth,
      }
    })
  const panState = (toEnd = false) =>
    page.locator('.xterm:not([data-aether-frozen-view] *)').evaluate((el, panToEnd) => {
      let owner = el.parentElement as HTMLElement | null
      while (owner && owner !== document.body && owner.scrollWidth <= owner.clientWidth) {
        owner = owner.parentElement
      }
      if (!owner || owner === document.body) throw new Error('terminal has no horizontal pan owner')
      const maximum = owner.scrollWidth - owner.clientWidth
      if (panToEnd) owner.scrollLeft = maximum
      return {
        left: owner.scrollLeft,
        maximum,
        top: owner.scrollTop,
        overflowY: getComputedStyle(owner).overflowY,
        pageTop: window.scrollY,
      }
    }, toEnd)
  expect(await grid()).toEqual({ cols: desktopCols, pannable: true })
  const initialPan = await panState(true)
  expect(initialPan.maximum).toBeGreaterThan(0)
  expect(initialPan.left).toBe(initialPan.maximum)
  expect(initialPan.top).toBe(0)
  expect(initialPan.overflowY).toBe('hidden')
  expect(initialPan.pageTop).toBe(0)

  // Taking over through Run Room is explicit because the desktop viewer
  // still owns the controller lease.
  const room = page.getByRole('complementary', { name: 'Run Room' })
  await page.getByRole('button', { name: 'Open Run Room' }).tap()
  await expect(room.getByText(/Controller: /)).toBeVisible()
  await room.getByRole('button', { name: 'Take control' }).tap()
  await page
    .getByRole('dialog', { name: 'Take control of this run?' })
    .getByRole('button', { name: 'Take control' })
    .tap()
  await expect(page.getByRole('button', { name: 'Steering' })).toBeVisible()
  await room.getByRole('button', { name: 'Close Run Room' }).tap()
  await expect(page.getByRole('toolbar', { name: 'Terminal keys' })).toBeVisible()
  expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })
  await expect(rows).toHaveCount(desktopRows)

  // Esc is the key an agent TUI needs most and the one no phone keyboard
  // has. It reaches the agent through the same path a keystroke takes.
  await page
    .getByRole('toolbar', { name: 'Terminal keys' })
    .getByRole('button', { name: 'Esc' })
    .tap()
  const focusedPan = await panState()
  expect(focusedPan.left).toBeGreaterThan(0)
  expect(focusedPan.top).toBe(0)
  expect(focusedPan.pageTop).toBe(0)

  // The soft keyboard shortens the layout, which is what would make a
  // terminal that fitted its pane re-fit and resize the session with it.
  const restore = await shrinkToKeyboardHeight(page)
  await expect(rows).toHaveCount(desktopRows)
  expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })
  const keyboardPan = await panState()
  expect(keyboardPan.left).toBeGreaterThan(0)
  expect(keyboardPan.top).toBe(0)
  expect(keyboardPan.overflowY).toBe('hidden')
  await restore()

  // The other way round: the desktop viewer's window changes, and the phone
  // follows it there without reattaching.
  desktop.close()
  const narrower = await attach(alice, run.id, {
    write: true,
    cols: 100,
    rows: 30,
    control_session_id: desktopSessionID,
    takeover: true,
  })
  expect(narrower.ack.ok).toBe(true)
  await expect(rows).toHaveCount(30)
  expect(await grid()).toEqual({ cols: 100, pannable: true })
  narrower.close()
})

test('a phone touch continues into history and across an older page without moving its anchor', async ({
  page,
  aether,
}, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', `stty -echo
IFS= read -r start
i=0
while [ "$i" -lt 2500 ]; do
  printf '\\033[2J\\033[HPHONE-HISTORY-%04d-${'x'.repeat(100)}\\r\\n' "$i"
  i=$((i + 1))
done
printf '\\033[2J\\033[HPHONE-HISTORY-CURRENT\\r\\n'
while IFS= read -r input; do
  printf 'UNEXPECTED-PHONE-INPUT:%s\\r\\n' "$input"
done`)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'touch through phone history',
  })
  const desktop = await attach(alice, run.id, {
    write: true,
    interactive: true,
    cols: desktopCols,
    rows: desktopRows,
    control_session_id: desktopSessionID,
  })
  expect(desktop.ack.ok).toBe(true)
  const sessionGeometry = async () => {
    const probe = await attach(alice, run.id, {
      follow: true,
      control_session_id: probeSessionID,
    })
    try {
      expect(probe.ack.ok).toBe(true)
      return { cols: probe.ack.cols, rows: probe.ack.rows }
    } finally {
      probe.close()
    }
  }

  let releaseOlder = () => {}
  const olderGate = new Promise<void>((resolve) => { releaseOlder = resolve })
  let delayedOlder = false
  let completedPages = 0
  let firstPageOldest = Number.POSITIVE_INFINITY
  const phoneInputs: (string | Buffer)[] = []
  page.on('websocket', (socket) => {
    if (!socket.url().includes(`/ws/attach/${run.id}`)) return
    socket.on('framesent', ({ payload }) => {
      if (typeof payload !== 'string' || JSON.parse(payload).type === 'input') {
        phoneInputs.push(payload)
      }
    })
  })
  await page.route('**/api/v1/terminal.history', async (route) => {
    const params = route.request().postDataJSON() as { before?: string; run_id: string }
    if (params.run_id !== run.id) return route.continue()
    const hold = !!params.before && !delayedOlder
    if (hold) delayedOlder = true
    const response = await route.fetch()
    const result = await response.json() as { lines: { text: string }[] }
    if (!params.before) {
      const markers = result.lines.flatMap((line) => {
        const match = /PHONE-HISTORY-(\d{4})-/.exec(line.text)
        return match ? [Number(match[1])] : []
      })
      expect(markers.length).toBeGreaterThan(0)
      firstPageOldest = Math.min(...markers)
    }
    if (hold) await olderGate
    await route.fulfill({ response })
    completedPages++
  })

  const cdp = await page.context().newCDPSession(page)
  let touching = false
  const paint = () => page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
  }))
  const touch = async (type: 'touchStart' | 'touchMove', x: number, y: number) => {
    await cdp.send('Input.dispatchTouchEvent', {
      type,
      touchPoints: [{ x, y, id: 1, radiusX: 2, radiusY: 2 }],
    })
    touching = true
    await paint()
  }
  const lift = async () => {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    touching = false
    await paint()
  }
  const scroller = page.getByLabel('Terminal scrollback', { exact: true })
  const viewport = () => scroller.evaluate((element) => {
    const box = element.getBoundingClientRect()
    const rows = Array.from(element.querySelectorAll<HTMLElement>('[data-history-row]'))
      .filter((row) => {
        const bounds = row.getBoundingClientRect()
        return bounds.bottom > box.top + element.clientTop && bounds.top < box.bottom
      })
      .map((row) => ({
        index: Number(row.dataset.historyRow),
        cursor: row.dataset.historyCursor ?? null,
        text: row.textContent,
        top: row.getBoundingClientRect().top - box.top,
      }))
    return { rows, left: element.scrollLeft }
  })

  try {
    await page.goto(alice.url)
    await page.getByRole('button', { name: 'Expand sidebar' }).tap()
    await page.getByRole('dialog', { name: 'Runs' })
      .getByRole('button', { name: 'touch through phone history' }).tap()
    await expect(page.getByText('Attached')).toBeVisible()
    desktop.sendInput('go\r')
    const live = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
    await expect(live).toContainText('PHONE-HISTORY-CURRENT', { timeout: 30_000 })
    await expect(live.locator(':scope > div')).toHaveCount(desktopRows)
    expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })

    // Browsing while steering must not focus xterm's keyboard or emit input.
    const room = page.getByRole('complementary', { name: 'Run Room' })
    await page.getByRole('button', { name: 'Open Run Room' }).tap()
    await room.getByRole('button', { name: 'Take control' }).tap()
    await page.getByRole('dialog', { name: 'Take control of this run?' })
      .getByRole('button', { name: 'Take control' }).tap()
    await expect(page.getByRole('button', { name: 'Steering' })).toBeVisible()
    await room.getByRole('button', { name: 'Close Run Room' }).tap()
    await page.evaluate(() => {
      document.documentElement.dataset.phoneHistoryInputFocus = ''
      document.addEventListener('focusin', (event) => {
        if (event.target instanceof HTMLElement &&
          event.target.matches('input, textarea, [contenteditable="true"]')) {
          document.documentElement.dataset.phoneHistoryInputFocus = event.target.tagName
        }
      })
    })
    const initialViewport = page.viewportSize()!
    const screen = await page.locator('.xterm-screen:not([data-aether-frozen-view] *):visible').boundingBox()
    if (!screen) throw new Error('phone terminal has no screen')
    const x = initialViewport.width / 2
    const top = Math.max(0, screen.y) + 40
    const bottom = Math.min(initialViewport.height, screen.y + screen.height) - 40
    expect(bottom - top).toBeGreaterThan(250)

    await touch('touchStart', x, top)
    await touch('touchMove', x, top + 60)
    await expect(scroller).toBeVisible()
    // The live redraws are still in native scrollback. Opening the surface
    // must not depend on fetching an archive page.
    await expect.poll(async () => (await viewport()).rows[0]?.index).toBeGreaterThan(0)
    const handoff = await viewport()
    expect(handoff.rows[0].cursor).toBeNull()
    // No second touchStart: the finger that opened history must keep moving it.
    for (let step = 1; step <= 6; step++) {
      await touch('touchMove', x, top + 60 + step * 30)
    }
    await expect.poll(async () => (await viewport()).rows[0]?.index)
      .toBeLessThan(handoff.rows[0].index - 5)
    await lift()

    // Each clear-screen redraw retains its nonempty row in xterm. Budget the
    // real touch travel from the native row distance, plus the archive swipes.
    const rowHeight = handoff.rows[1].top - handoff.rows[0].top
    const nativeSwipes = Math.ceil((handoff.rows[0].index + 1) * rowHeight / (bottom - top))
    for (let swipe = 0; swipe < nativeSwipes + 24 && !delayedOlder; swipe++) {
      await touch('touchStart', x, top)
      for (let step = 1; step <= 12 && !delayedOlder; step++) {
        await touch('touchMove', x, top + (bottom - top) * step / 12)
      }
      if (!delayedOlder) await lift()
    }
    expect(delayedOlder).toBe(true)
    await expect.poll(() => completedPages, { timeout: 15_000 }).toBeGreaterThan(0)
    // Keep a finger down while releasing the real response, so momentum cannot
    // be mistaken for a prepend jump.
    if (!touching) await touch('touchStart', x, top)
    await expect.poll(async () => (await viewport()).rows[0]?.cursor).toBeTruthy()
    const beforePrepend = await viewport()
    const heightBefore = await scroller.evaluate((element) => element.scrollHeight)
    const pagesBefore = completedPages
    releaseOlder()
    await expect.poll(() => completedPages, { timeout: 15_000 }).toBeGreaterThan(pagesBefore)
    await expect.poll(() => scroller.evaluate((element) => element.scrollHeight))
      .toBeGreaterThan(heightBefore)
    await expect.poll(viewport).toEqual(beforePrepend)
    await lift()

    const olderMarkerVisible = async () => (await viewport()).rows.some((row) => {
      const marker = /PHONE-HISTORY-(\d{4})-/.exec(row.text ?? '')
      return row.cursor !== null && marker !== null && Number(marker[1]) < firstPageOldest
    })
    for (let swipe = 0; swipe < 12 && !await olderMarkerVisible(); swipe++) {
      await touch('touchStart', x, top)
      for (let step = 1; step <= 12; step++) {
        await touch('touchMove', x, top + (bottom - top) * step / 12)
      }
      await lift()
    }
    expect(await olderMarkerVisible()).toBe(true)
    expect(firstPageOldest).toBeGreaterThan(1000)
    expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })

    const beforePan = await viewport()
    const panY = top + (bottom - top) / 2
    await touch('touchStart', initialViewport.width - 50, panY)
    for (let step = 1; step <= 10; step++) {
      await touch('touchMove', initialViewport.width - 50 - step * 25, panY)
    }
    await lift()
    await expect.poll(async () => (await viewport()).left).toBeGreaterThan(beforePan.left + 50)
    await expect(page.locator('html')).toHaveAttribute('data-phone-history-input-focus', '')
    await expect(page.locator('input:focus, textarea:focus, [contenteditable="true"]:focus')).toHaveCount(0)
    expect(phoneInputs).toEqual([])
    expect(page.viewportSize()).toEqual(initialViewport)
    expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })
    await testInfo.attach('phone older history after touch and horizontal pan', {
      body: await page.screenshot({ fullPage: true }),
      contentType: 'image/png',
    })
  } finally {
    releaseOlder()
    if (touching) await cdp.send('Input.dispatchTouchEvent', { type: 'touchCancel', touchPoints: [] })
    await cdp.detach()
    desktop.close()
  }
})
