import { writeFileSync } from 'node:fs'
import path from 'node:path'
import type { Locator, Page } from '@playwright/test'
import type { Aether, Member } from './fixtures'
import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

const openWriter = async (
  url: URL,
  runID: string,
  scriptID: string,
): Promise<{ socket: WebSocket; generation: () => number }> => {
  const socket = new WebSocket(
    `ws://${url.host}/ws/attach/${runID}?token=${url.searchParams.get('token')}`,
  )
  socket.binaryType = 'arraybuffer'
  let generation = 0
  await new Promise<void>((resolve, reject) => {
    const fail = (error: Error) => reject(error)
    socket.addEventListener('error', () => fail(new Error(`${scriptID} writer socket failed`)))
    socket.addEventListener('close', () => fail(new Error(`${scriptID} writer closed before ack`)))
    socket.addEventListener('open', () => {
      socket.send(
        JSON.stringify({
          write: true,
          screen: false,
          interactive: true,
          cols: 80,
          rows: 24,
          control_session_id: scriptID,
        }),
      )
    })
    socket.addEventListener('message', (event) => {
      if (typeof event.data !== 'string') return
      let ack: { ok?: boolean; error?: string; control_generation?: number }
      try {
        ack = JSON.parse(event.data) as typeof ack
      } catch {
        return
      }
      if (ack.ok === undefined) return
      if (!ack.ok) reject(new Error(ack.error ?? `${scriptID} writer refused`))
      else {
        generation = ack.control_generation ?? 0
        resolve()
      }
    })
  })
  return { socket, generation: () => generation }
}

async function closeWriter(socket: WebSocket): Promise<void> {
  if (socket.readyState === WebSocket.CLOSED) return
  await new Promise<void>((resolve) => {
    socket.addEventListener('close', () => resolve(), { once: true })
    socket.close()
  })
}
async function releaseWriter(
  writer: { socket: WebSocket; generation: () => number },
  requestID: number,
): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`writer control request ${requestID} timed out`)), 30_000)
    const onMessage = (event: MessageEvent) => {
      if (typeof event.data !== 'string') return
      let frame: { type?: string; request_id?: number; ok?: boolean; error?: string }
      try {
        frame = JSON.parse(event.data) as typeof frame
      } catch {
        return
      }
      if (frame.type !== 'control' || frame.request_id !== requestID) return
      clearTimeout(timeout)
      writer.socket.removeEventListener('message', onMessage)
      if (frame.ok) resolve()
      else reject(new Error(frame.error ?? `writer control request ${requestID} refused`))
    }
    writer.socket.addEventListener('message', onMessage)
    writer.socket.send(
      JSON.stringify({
        type: 'control',
        request_id: requestID,
        write: false,
        control_generation: writer.generation(),
      }),
    )
  })
}
async function sendInputUntil(
  writer: { socket: WebSocket; generation: () => number },
  input: string,
  marker: string,
): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`writer did not produce ${marker}`)), 180_000)
    let output = ''
    const onMessage = (event: MessageEvent) => {
      if (typeof event.data === 'string') return
      output = (output + new TextDecoder().decode(new Uint8Array(event.data as ArrayBuffer))).slice(-8192)
      if (!output.includes(marker)) return
      clearTimeout(timeout)
      writer.socket.removeEventListener('message', onMessage)
      resolve()
    }
    writer.socket.addEventListener('message', onMessage)
    writer.socket.send(
      JSON.stringify({
        type: 'input',
        data: input,
        control_generation: writer.generation(),
      }),
    )
  })
}


test('take and release change input authority without replacing the output socket', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(
    await memberID(alice),
    'claude',
    `stty -echo
IFS= read -r start
printf 'CONTROL-BASE\\r\\n'
while IFS= read -r line; do
  printf '\\r\\nCONTROL-ECHO:%s\\r\\n' "$line"
done
`,
  )
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'same socket control',
  })
  const url = new URL(alice.url)
  const bootstrap = await openWriter(url, run.id, 'control-bootstrap')
  await sendInputUntil(bootstrap, 'go\r', 'CONTROL-BASE')
  await releaseWriter(bootstrap, 1)
  await closeWriter(bootstrap.socket)

  let outputSockets = 0
  let binaryBytes = 0
  const frames: Array<{ type?: string; write?: boolean; request_id?: number }> = []
  await page.routeWebSocket(/\/ws\/attach\//, (socket) => {
    outputSockets++
    const server = socket.connectToServer()
    socket.onMessage((message) => {
      if (typeof message === 'string') {
        try {
          const frame = JSON.parse(message) as { type?: string; write?: boolean; request_id?: number }
          frames.push(frame)
        } catch {
          // The live output path remains connected to the real server.
        }
      }
      server.send(message)
    })
    server.onMessage((message) => {
      if (typeof message !== 'string') binaryBytes += message.length
      socket.send(message)
    })
  })
  await page.goto(alice.url)
  await page
    .getByRole('complementary', { name: 'Runs' })
    .getByRole('button', { name: /same socket control/ })
    .click()
  const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
  await expect(rows).toContainText('CONTROL-BASE', { timeout: 30_000 })
  const settled = await rows.textContent()
  const socketsBeforeControl = outputSockets
  const bytesBeforeControl = binaryBytes

  await page.getByRole('button', { name: 'Steering', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()
  await expect
    .poll(() => frames.filter((frame) => frame.type === 'control' && frame.write === false).length)
    .toBe(1)
  expect(outputSockets).toBe(socketsBeforeControl)
  expect(binaryBytes).toBe(bytesBeforeControl)
  await expect.poll(() => rows.textContent()).toBe(settled)
  await expect(page.getByRole('status', { name: 'Restoring terminal history' })).toBeHidden()
  await page.locator('.xterm-screen:not([data-aether-frozen-view] *)').click()
  await page.keyboard.type('read-only-check')
  await page.keyboard.press('Enter')
  await page.waitForTimeout(500)
  expect(await rows.textContent()).not.toContain('CONTROL-ECHO:read-only-check')

  await page.getByRole('button', { name: 'Take control', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Steering', exact: true })).toBeVisible()
  await expect
    .poll(() => frames.filter((frame) => frame.type === 'control' && frame.write === true).length)
    .toBe(1)
  expect(outputSockets).toBe(socketsBeforeControl)
  expect(binaryBytes).toBe(bytesBeforeControl)
  await expect.poll(() => rows.textContent()).toBe(settled)

  await page.locator('.xterm-screen:not([data-aether-frozen-view] *)').click()
  await page.keyboard.type('authority-check')
  await page.keyboard.press('Enter')
  await expect(rows).toContainText('CONTROL-ECHO:authority-check', { timeout: 15_000 })
  expect(outputSockets).toBe(socketsBeforeControl)
})

const historyAgent = `stty -echo
IFS= read -r config
set -- $config
label=$1
count=$2
printf '\\033[2J\\033[HEARLIEST-%s\\r\\n' "$label"
i=0
while [ "$i" -lt "$count" ]; do
  printf '\\033[2J\\033[H%s-HISTORY-%05d-${'x'.repeat(220)}\\r\\n' "$label" "$i"
  i=$((i + 1))
done
printf '\\033[2J\\033[H%s-CURRENT\\r\\n' "$label"
while IFS= read -r command; do
  if [ "$command" = "stream" ]; then
    (
      i=0
      while [ "$i" -lt 1200 ]; do
        printf '\\033[2J\\033[H%s-GAP-%05d\\r\\n' "$label" "$i"
        i=$((i + 1))
        sleep 0.1
      done
    ) &
    stream_pid=$!
  elif [ "$command" = "query" ]; then
    i=0
    while [ "$i" -lt 2400 ]; do
      printf '%s-NATIVE-%05d\\r\\n' "$label" "$i"
      i=$((i + 1))
    done
    printf '%s-CURRENT\\r\\n' "$label"
    mode=$(stty -g)
    stty raw -echo
    printf '%s-QUERY-READY\\r\\n' "$label"
    until [ -f "$HOME/.history-$label-query" ]; do sleep 0.05; done
    printf '\\033[6n'
    response=''
    while :; do
      byte=$(dd bs=1 count=1 2>/dev/null)
      response="$response$byte"
      [ "$byte" = R ] && break
    done
    stty "$mode"
    printf '%s-CPR:%s\\r\\n' "$label" "$response"
  elif [ "$command" = "refresh" ]; then
    kill "$stream_pid"
    printf '\\033[2J\\033[H%s-REFRESH-FIRST\\r\\n' "$label"
    i=0
    while [ "$i" -lt 7200 ]; do
      printf '\\033[2J\\033[H%s-REFRESH-%05d-${'y'.repeat(220)}\\r\\n' "$label" "$i"
      i=$((i + 1))
    done
    printf '\\033[2J\\033[H%s-REFRESH-CURRENT\\r\\n' "$label"
  else
    printf '%s-INPUT:%s\\r\\n' "$label" "$command"
  fi
done
`

async function launchHistoryRun(
  alice: Member,
  workspaceID: string,
  label: string,
  count: number,
) {
  const task = `scrollable history ${label}`
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaceID,
    harness: 'claude',
    task,
  })
  const writer = await openWriter(new URL(alice.url), run.id, `history-${label}`)
  await sendInputUntil(writer, `${label} ${count}\r`, `${label}-CURRENT`)
  return { id: run.id, task, writer }
}

async function prepareHistory(aether: Aether) {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', historyAgent)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  return { alice, workspaceID: workspaces[0].id }
}

function visibleHistory(scroller: Locator) {
  return scroller.evaluate((element) => {
    const viewport = element.getBoundingClientRect()
    const visible = Array.from(element.querySelectorAll<HTMLElement>('[data-history-row]'))
      .filter((row) => {
        const bounds = row.getBoundingClientRect()
        return bounds.bottom > viewport.top + element.clientTop && bounds.top < viewport.bottom
      })
      .map((row) => ({
        index: Number(row.dataset.historyRow),
        cursor: row.dataset.historyCursor ?? null,
        text: row.textContent,
        top: row.getBoundingClientRect().top - viewport.top,
        left: row.getBoundingClientRect().left - viewport.left,
      }))
    return { rows: visible, left: element.scrollLeft }
  })
}

async function wheelUntil(
  page: Page,
  scroller: Locator,
  delta: number,
  reached: () => Promise<boolean>,
) {
  await scroller.hover()
  await expect.poll(async () => {
    if (await reached()) return true
    await page.mouse.wheel(0, delta)
    return false
  }, { timeout: 60_000, intervals: [100, 250] }).toBe(true)
}

test('scrolling reaches every retained page and prepends without moving visible output', async ({
  page,
  aether,
}, testInfo) => {
  const { alice, workspaceID } = await prepareHistory(aether)
  const run = await launchHistoryRun(alice, workspaceID, 'LONG', 12000)
  let observedOutput = ''
  run.writer.socket.addEventListener('message', (event) => {
    if (typeof event.data !== 'string') {
      observedOutput += new TextDecoder().decode(new Uint8Array(event.data as ArrayBuffer))
    }
  })
  // Keep a read-only output observer, never a second terminal parser or writer.
  await releaseWriter(run.writer, 1)
  const queryGate = path.join(aether.server.memberHome(await memberID(alice)), '.history-LONG-query')
  let releaseOlder = () => {}
  const olderGate = new Promise<void>((resolve) => { releaseOlder = resolve })
  let delayedOlder = false
  let deliveredRows = 0
  let completedPages = 0
  const rawDownloads: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.endsWith('/terminal-history')) rawDownloads.push(request.url())
  })
  await page.route('**/api/v1/terminal.history', async (route) => {
    const params = route.request().postDataJSON() as { before?: string; query?: string }
    expect(params.query ?? '').toBe('')
    const hold = !!params.before && !delayedOlder
    if (hold) delayedOlder = true
    const response = await route.fetch()
    if (hold) await olderGate
    const result = await response.json() as { lines: unknown[] }
    deliveredRows += result.lines.length
    await route.fulfill({ response })
    completedPages++
  })
  try {
    await page.goto(alice.url)
    await page.getByRole('complementary', { name: 'Runs' })
      .getByRole('button', { name: run.task }).click()
    const live = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
    await expect(live).toContainText('LONG-CURRENT', { timeout: 30_000 })
    await expect(live).not.toContainText('EARLIEST-LONG')
    await expect(page.getByRole('button', { name: 'Steering', exact: true })).toBeVisible()
    await page.locator('.xterm-screen:not([data-aether-frozen-view] *):visible').click()
    await page.keyboard.type('query')
    await page.keyboard.press('Enter')
    await expect.poll(() => observedOutput).toContain('LONG-QUERY-READY')
    // These rows arrive through the attached live PTY, not the initial replay.
    // Seeing the final marker proves xterm parsed the entire scrollback burst.
    await expect(live).toContainText('LONG-QUERY-READY', { timeout: 30_000 })

    // Drag xterm's shipped SmoothScrollableElement slider, not scrollTop or
    // a synthetic scroll event. Hover exposes its auto-hidden vertical bar.
    // All moves share one held button; activating history must not steal it.
    const nativeViewport = page.locator(
      '.xterm-scrollable-element:not([data-aether-frozen-view] *):visible',
    )
    await nativeViewport.hover()
    const nativeScrollbar = nativeViewport.locator(':scope > .scrollbar.vertical.visible')
    const nativeSlider = nativeScrollbar.locator(':scope > .slider')
    await expect(nativeSlider).toBeVisible()
    const track = await nativeScrollbar.boundingBox()
    const thumb = await nativeSlider.boundingBox()
    if (!track || !thumb) throw new Error('Live xterm scrollbar has no bounding box')
    expect(track.height).toBeGreaterThan(thumb.height * 2)
    const dragX = thumb.x + thumb.width / 2
    const dragStartY = thumb.y + thumb.height / 2
    const dragTravel = thumb.y - track.y
    expect(dragTravel).toBeGreaterThan(track.height / 2)
    const nativeRows = () => live.locator(':scope > div').evaluateAll((rows) =>
      rows.map((row) => row.textContent?.trimEnd() ?? '').filter(Boolean),
    )
    await page.mouse.move(dragX, dragStartY)
    await page.mouse.down()
    let previousRows = await nativeRows()
    let finalRows = previousRows
    let finalOrigin: { top: number; left: number } | null = null
    try {
      for (const fraction of [0.15, 0.3, 0.45]) {
        await page.mouse.move(dragX, dragStartY - dragTravel * fraction, { steps: 4 })
        await expect.poll(nativeRows).not.toEqual(previousRows)
        previousRows = await nativeRows()
      }
      finalRows = previousRows
      const box = await live.locator(':scope > div').first().boundingBox()
      if (!box) throw new Error('Final native row has no visible geometry')
      finalOrigin = { top: box.y, left: box.x }
    } finally {
      await page.mouse.up()
    }
    const scroller = page.getByLabel('Terminal scrollback', { exact: true })
    await expect(scroller).toBeVisible()
    await expect.poll(async () => (await visibleHistory(scroller)).rows
      .map((row) => row.text?.trimEnd() ?? '').filter(Boolean).slice(0, 3))
      .toEqual(finalRows.slice(0, 3))
    await expect.poll(async () => {
      const row = (await visibleHistory(scroller)).rows[0]
      const box = await scroller.boundingBox()
      return row && box ? { top: box.y + row.top, left: box.x + row.left } : null
    }).toEqual(finalOrigin)

    const beforeQuery = await visibleHistory(scroller)
    await scroller.focus()
    await page.keyboard.type('forbidden-history-input')
    await page.keyboard.press('Enter')
    // The PTY is already in raw mode and waiting on a mounted-file handshake,
    // so any leaked keystrokes precede (and corrupt) its actual CSI 6n reply.
    writeFileSync(queryGate, 'send cursor query')
    await expect.poll(() => observedOutput).toMatch(/LONG-CPR:\x1b\[\d+;\d+R/)
    expect(observedOutput).not.toContain('forbidden-history-input')
    await expect(scroller).toBeVisible()
    await expect.poll(() => visibleHistory(scroller)).toEqual(beforeQuery)
    await wheelUntil(page, scroller, -2400, async () => delayedOlder)
    await expect.poll(async () => (await visibleHistory(scroller)).rows.length).toBeGreaterThan(0)
    const beforePrepend = await visibleHistory(scroller)
    const pagesBeforePrepend = completedPages
    releaseOlder()
    await expect.poll(() => completedPages).toBeGreaterThan(pagesBeforePrepend)
    await expect.poll(() => visibleHistory(scroller)).toEqual(beforePrepend)

    await wheelUntil(page, scroller, -8000, async () =>
      (await visibleHistory(scroller)).rows.some((row) => row.text?.includes('EARLIEST-LONG')),
    )
    expect(deliveredRows).toBeGreaterThan(12000)
    expect(completedPages).toBeGreaterThan(60)
    expect(await scroller.locator('[data-history-row]').count()).toBeLessThan(250)
    await testInfo.attach('earliest retained history', {
      body: await page.screenshot({ fullPage: true }),
      contentType: 'image/png',
    })
    const earliest = await visibleHistory(scroller)
    expect(earliest.rows[0].index).toBeLessThan(-10000)
    expect(earliest.rows.find((row) => row.text?.includes('EARLIEST-LONG'))?.cursor).toBeTruthy()

    // Keep the real Clipboard object for observation while making the page's
    // async clipboard API unavailable. Observe native clipboard contents,
    // never a mocked write or an invocation of the fallback helper.
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], {
      origin: new URL(alice.url).origin,
    })
    const clipboard = await page.evaluateHandle(() => navigator.clipboard)
    try {
      await clipboard.evaluate((native) => native.writeText('clipboard-sentinel'))
      await page.evaluate(() => {
        Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
      })
      const earliestRow = scroller.locator('[data-history-row]').filter({ hasText: 'EARLIEST-LONG' })
      const rowBox = await earliestRow.boundingBox()
      expect(rowBox).not.toBeNull()
      await page.mouse.move(rowBox!.x + 1, rowBox!.y + rowBox!.height / 2)
      await page.mouse.down()
      await page.mouse.move(rowBox!.x + 150, rowBox!.y + rowBox!.height / 2, { steps: 5 })
      await page.mouse.up()
      const selected = await page.evaluate(() => window.getSelection()?.toString() ?? '')
      expect(selected).toContain('EARLIEST-LONG')
      await page.getByRole('button', { name: 'Copy terminal selection', exact: true }).click()
      await expect.poll(() => clipboard.evaluate((native) => native.readText())).toBe(selected)

      await clipboard.evaluate((native) => native.writeText('clipboard-sentinel'))
      await page.evaluate(() => {
        Object.defineProperty(navigator, 'clipboard', {
          configurable: true,
          value: {
            writeText: async () => { throw new DOMException('Clipboard denied', 'NotAllowedError') },
          },
        })
      })
      const screenText = (await visibleHistory(scroller)).rows.map((row) => row.text ?? '').join('\n')
      expect(screenText).toContain('EARLIEST-LONG')
      await page.getByRole('button', { name: 'Copy last screen', exact: true }).click()
      await expect.poll(() => clipboard.evaluate((native) => native.readText())).toBe(screenText)
    } finally {
      await page.evaluate(() => { Reflect.deleteProperty(navigator, 'clipboard') })
      await clipboard.dispose()
    }

    await scroller.focus()
    await page.keyboard.press('PageDown')
    await expect.poll(async () => (await visibleHistory(scroller)).rows[0]?.index)
      .toBeGreaterThan(earliest.rows[0].index)
    const pagesAtOldest = completedPages
    await wheelUntil(page, scroller, 6000, async () => {
      const first = (await visibleHistory(scroller)).rows[0]
      return first !== undefined && first.index > -600
    })
    await wheelUntil(page, scroller, 200, async () =>
      await scroller.getByRole('separator').count() === 1,
    )
    await expect(scroller.getByRole('separator')).toHaveCount(1)
    expect(completedPages).toBe(pagesAtOldest)
    expect(await scroller.locator('[data-history-row]').count()).toBeLessThan(250)
    await scroller.focus()
    await page.keyboard.press('End')
    await expect(scroller).toBeHidden()
    await expect(live).toContainText('LONG-CURRENT')
    // End must hand keyboard focus back as part of leaving the reading surface.
    // No click/focus call is allowed between End and this real PTY command.
    await page.keyboard.type('after-history')
    await page.keyboard.press('Enter')
    await expect(live).toContainText('LONG-INPUT:after-history')
    expect(rawDownloads).toEqual([])
  } finally {
    releaseOlder()
    writeFileSync(queryGate, 'release query during teardown')
    await closeWriter(run.writer.socket)
  }
})

test('switching live runs restores the same recorded rows and pixel offsets without inactive sockets', async ({
  page,
  aether,
}, testInfo) => {
  const { alice, workspaceID } = await prepareHistory(aether)
  const runA = await launchHistoryRun(alice, workspaceID, 'RESTORE-A', 7200)
  const runB = await launchHistoryRun(alice, workspaceID, 'RESTORE-B', 0)
  const activeSockets = new Map<object, string>()
  const historyRequests: string[] = []
  page.on('websocket', (socket) => {
    const url = new URL(socket.url())
    if (!url.pathname.startsWith('/ws/attach/') || url.searchParams.has('shell')) return
    activeSockets.set(socket, url.pathname.split('/').pop()!)
    socket.on('close', () => activeSockets.delete(socket))
  })
  page.on('request', (request) => {
    if (new URL(request.url()).pathname !== '/api/v1/terminal.history') return
    const params = request.postDataJSON() as { run_id: string; before?: string }
    if (params.run_id === runA.id) historyRequests.push(params.before ?? '')
  })
  try {
    await page.goto(alice.url)
    const sidebar = page.getByRole('complementary', { name: 'Runs' })
    await sidebar.getByRole('button', { name: runA.task }).click()
    const live = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
    await expect(live).toContainText('RESTORE-A-CURRENT', { timeout: 30_000 })
    await expect.poll(() => [...activeSockets.values()]).toEqual([runA.id])
    await page.locator('.xterm-screen:not([data-aether-frozen-view] *):visible').click()
    await page.keyboard.press('Shift+PageUp')
    const scroller = page.getByLabel('Terminal scrollback', { exact: true })
    await expect(scroller).toBeVisible()
    await wheelUntil(page, scroller, -8000, async () =>
      (await visibleHistory(scroller)).rows.some((row) => row.index < -5200),
    )
    // Move away from the paging threshold, then pan a long unwrapped archive
    // line and leave its first row partially clipped.
    await scroller.hover()
    await page.mouse.wheel(137, 413)
    await expect.poll(async () => (await visibleHistory(scroller)).left).toBeGreaterThan(0)
    await expect.poll(async () => (await visibleHistory(scroller)).rows[0]?.top).toBeLessThan(0)
    const saved = await visibleHistory(scroller)
    expect(saved.rows[0].cursor).toBeTruthy()
    expect(saved.rows[0].text).toContain('RESTORE-A-HISTORY-')

    await sidebar.getByRole('button', { name: runB.task }).click()
    await expect(live).toContainText('RESTORE-B-CURRENT')
    await expect.poll(() => [...activeSockets.values()]).toEqual([runB.id])
    const requestsBeforeReturn = historyRequests.length
    await sendInputUntil(runA.writer, 'stream\r', 'RESTORE-A-GAP-00010')
    await expect(live).not.toContainText('RESTORE-A')
    expect(historyRequests).toHaveLength(requestsBeforeReturn)
    await sidebar.getByRole('button', { name: runA.task }).click()
    await expect(scroller).toBeVisible()
    await expect.poll(() => visibleHistory(scroller)).toEqual(saved)
    await expect.poll(() => [...activeSockets.values()]).toEqual([runA.id])
    expect(historyRequests).toHaveLength(requestsBeforeReturn)
    expect(await scroller.locator('[data-history-row]').count()).toBeLessThan(250)
    await testInfo.attach('restored run A while live output continues', {
      body: await page.screenshot({ fullPage: true }),
      contentType: 'image/png',
    })

    await wheelUntil(page, scroller, -8000, async () =>
      (await visibleHistory(scroller)).rows.some((row) => row.text?.includes('EARLIEST-RESTORE-A')),
    )
    const oldest = (await visibleHistory(scroller)).rows[0].index
    await scroller.focus()
    await page.keyboard.press('PageDown')
    await expect.poll(async () => (await visibleHistory(scroller)).rows[0]?.index).toBeGreaterThan(oldest)
    await page.keyboard.press('End')
    await expect(scroller).toBeHidden()
    await expect(live).toContainText('RESTORE-A-GAP-')
    // A new reading episode must start at a fresh archive head, not skip the
    // output that has already fallen out of xterm's bounded native buffer.
    await sendInputUntil(runA.writer, 'refresh\r', 'RESTORE-A-REFRESH-CURRENT')
    await expect(live).toContainText('RESTORE-A-REFRESH-CURRENT')
    await expect(live).not.toContainText('RESTORE-A-REFRESH-FIRST')
    const headsBeforeRefresh = historyRequests.filter((cursor) => cursor === '').length
    await page.locator('.xterm-screen:not([data-aether-frozen-view] *):visible').hover()
    await page.mouse.wheel(0, -2400)
    await expect(scroller).toBeVisible()
    // The refresh filled xterm's 5000-row native buffer. Traverse it before
    // requiring the new episode to fetch the newest archive page.
    await wheelUntil(page, scroller, -8000, async () =>
      historyRequests.filter((cursor) => cursor === '').length > headsBeforeRefresh,
    )
    await wheelUntil(page, scroller, -8000, async () =>
      (await visibleHistory(scroller)).rows.some((row) => row.index < -6900),
    )
    await wheelUntil(page, scroller, -200, async () =>
      (await visibleHistory(scroller)).rows.some((row) =>
        row.cursor !== null && row.text?.includes('RESTORE-A-REFRESH-FIRST')),
    )
    await testInfo.attach('fresh archive head after returning live', {
      body: await page.screenshot({ fullPage: true }),
      contentType: 'image/png',
    })
    await page.getByRole('tab', { name: 'Overview', exact: true }).click()
    await expect.poll(() => activeSockets.size).toBe(0)
  } finally {
    await releaseWriter(runA.writer, 1)
    await closeWriter(runA.writer.socket)
    await releaseWriter(runB.writer, 1)
    await closeWriter(runB.writer.socket)
  }
})
