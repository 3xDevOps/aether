import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// Cursor-addressed output exposes a wrong grid where plain echo does not.
const painter = `stty -echo
paint() {
  set -- $(stty size)
  # The desktop pane is taller than the fitted grid, so the viewport does not
  # overflow until the clear pushes these lines into scrollback.
  i=0
  while [ "$i" -lt 160 ]; do
    printf 'scrollback %s\\n' "$i"
    i=$((i + 1))
  done
  printf '\\033[2J\\033[H'
  printf '\\033[%s;%sHX' "$1" "$2"
}
trap paint WINCH
paint
while :; do
  if IFS= read -r line; then paint; fi
done
`
const geometrySessionID = 'terminal-geometry-writer'
const historySessionID = 'terminal-history-writer'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('new runs keep desktop viewers on the shared grid through resize and reattach', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', painter)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'shared geometry regression',
  })
  const url = new URL(alice.url)
  const writer = new WebSocket(`ws://${url.host}/ws/attach/${run.id}?token=${url.searchParams.get('token')}`)
  let writerGeneration = 0
  try {
    await new Promise<void>((resolve, reject) => {
      writer.addEventListener('error', () => reject(new Error('writer socket failed')))
      writer.addEventListener('open', () =>
        writer.send(
          JSON.stringify({
            write: true,
            screen: true,
            interactive: true,
            cols: 60,
            rows: 18,
            control_session_id: geometrySessionID,
          }),
        ),
      )
      writer.addEventListener('message', (event) => {
        if (typeof event.data !== 'string') return
        const ack = JSON.parse(event.data)
        if (!ack.ok) reject(new Error(ack.error ?? 'writer refused'))
        else {
          writerGeneration = ack.control_generation ?? 0
          resolve()
        }
      }, { once: true })
    })
    await page.setViewportSize({ width: 1568, height: 1000 })
    await page.goto(alice.url)
    await page
      .getByRole('complementary', { name: 'Runs' })
      .getByRole('button', { name: /shared geometry regression/ })
      .click()
    const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible > div')
    const assertGrid = async (cols: number, height: number) => {
      await expect(rows).toHaveCount(height)
      writer.send(JSON.stringify({ type: 'input', data: '\r', control_generation: writerGeneration }))
      await expect(rows.nth(height - 1)).toHaveText(`${' '.repeat(cols - 1)}X`)
    }
    await assertGrid(60, 18)
    writer.send(JSON.stringify({ type: 'resize', cols: 72, rows: 22 }))
    await assertGrid(72, 22)
    // The larger viewer must keep proposing its pane, not feed 60x18 back
    // into the minimum and stop the shared terminal ever growing again.
    // xterm 6 leaves .xterm-viewport empty and scrolls a custom thumb, so a
    // scrollTop written there never moves. Rows still below the fold are the
    // thumb's distance from the bottom of its track.
    const terminal = rows.nth(0).locator('xpath=ancestor::*[contains(concat(" ", normalize-space(@class), " "), " xterm ")][1]')
    const thumb = terminal.locator('.xterm-scrollable-element > .scrollbar.vertical > .slider')
    const rowsBelow = () =>
      thumb.evaluate((element) => {
        const track = element.parentElement
        const row = element.closest('.xterm')?.querySelector('.xterm-rows > div')
        const rowHeight = row?.getBoundingClientRect().height ?? 0
        if (!track || rowHeight <= 0) return 0
        const trackBox = track.getBoundingClientRect()
        const thumbBox = element.getBoundingClientRect()
        if (thumbBox.height <= 0 || trackBox.height <= 0) return 0
        const visibleRows = trackBox.height / rowHeight
        return ((trackBox.height - (thumbBox.top - trackBox.top) - thumbBox.height) / thumbBox.height) * visibleRows
      })
    await expect.poll(() =>
      thumb.evaluate((element) => {
        const track = element.parentElement?.getBoundingClientRect().height ?? 0
        const height = element.getBoundingClientRect().height
        const row = element.closest('.xterm')?.querySelector('.xterm-rows > div')?.getBoundingClientRect().height ?? 0
        if (row <= 0) return 0
        return (track - height) / row
      }),
    ).toBeGreaterThan(2)
    await terminal.hover()
    let pinnedRows = 0
    await expect.poll(async () => {
      // One notch is about three rows. Repeat only while the thumb is still
      // on the bottom, and keep the first position that is actually pinned.
      if (pinnedRows <= 2) await page.mouse.wheel(0, -1)
      pinnedRows = await rowsBelow()
      return pinnedRows
    }).toBeGreaterThan(2)
    await page.getByRole('button', { name: 'Increase terminal text size' }).click()
    await expect.poll(rowsBelow).toBeCloseTo(pinnedRows, 0)
    // The pin leaves the live row above the fold, and the font-size click
    // leaves the pointer on that button. Half a row still counts as pinned,
    // so the next paint stays below the fold. Wheel until the thumb is flush
    // with the bottom of the track.
    await expect.poll(async () => {
      await terminal.hover()
      const below = await rowsBelow()
      if (below > 0.05) await page.mouse.wheel(0, 1)
      return below
    }).toBeLessThan(0.05)
    await assertGrid(72, 22)
    await expect(page.getByRole('button', { name: 'Take control' })).toBeVisible()
    await assertGrid(72, 22)
    await page.getByRole('tab', { name: 'Overview', exact: true }).click()
    await page.getByRole('tab', { name: 'Terminal', exact: true }).click()
    await assertGrid(72, 22)
  } finally {
    writer.close()
  }
})

test('fresh runs bootstrap a bounded current screen after a large redraw archive', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  const firstOutput = 'HISTORY-FIRST-OUTPUT'
  const currentPrompt = 'CURRENT-SNAPSHOT-PROMPT> '
  const redrawFill = 'x'.repeat(96)
  const historyAgent = `stty -echo
IFS= read -r start
printf '\\033[2J\\033[H${firstOutput}\\n'
sleep 1
i=0
while [ "$i" -lt 18000 ]; do
  printf '\\033[2J\\033[HOLD-REDRAW-%05d-${redrawFill}\\n' "$i"
  i=$((i + 1))
done
printf '\\033[2J\\033[H${currentPrompt}'
while IFS= read -r line; do
  printf '\\r\\nLIVE:%s\\r\\n${currentPrompt}' "$line"
done
`
  aether.installAgent(await memberID(alice), 'claude', historyAgent)

  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'snapshot current prompt',
  })

  const url = new URL(alice.url)
  const writer = new WebSocket(`ws://${url.host}/ws/attach/${run.id}?token=${url.searchParams.get('token')}`)
  writer.binaryType = 'arraybuffer'
  let writerGeneration = 0
  let generatedBytes = 0
  try {
    await new Promise<void>((resolve, reject) => {
      let started = false
      let settled = false
      let text = ''
      let timeout: NodeJS.Timeout
      const fail = (error: Error) => {
        if (settled) return
        settled = true
        clearTimeout(timeout)
        reject(error)
      }
      timeout = setTimeout(() => fail(new Error('agent did not finish its redraws')), 180_000)
      writer.addEventListener('error', () => fail(new Error('writer socket failed')))
      writer.addEventListener('close', () => {
        if (!settled) fail(new Error('writer socket closed before the prompt'))
      })
      writer.addEventListener('open', () => {
        writer.send(
          JSON.stringify({
            write: true,
            screen: false,
            interactive: true,
            cols: 80,
            rows: 24,
            control_session_id: historySessionID,
          }),
        )
      })
      writer.addEventListener('message', (event) => {
        if (typeof event.data === 'string') {
          let ack: { ok?: boolean; error?: string; control_generation?: number }
          try {
            ack = JSON.parse(event.data) as typeof ack
          } catch {
            return
          }
          if (ack.ok === undefined) return
          if (!ack.ok) {
            fail(new Error(ack.error ?? 'writer refused'))
          } else if (!started) {
            started = true
            writerGeneration = ack.control_generation ?? 0
            writer.send(JSON.stringify({ type: 'input', data: 'go\r', control_generation: writerGeneration }))
          }
          return
        }
        const chunk = new Uint8Array(event.data as ArrayBuffer)
        generatedBytes += chunk.byteLength
        text = (text + new TextDecoder().decode(chunk)).slice(-8192)
        if (text.includes(currentPrompt) && started) {
          settled = true
          clearTimeout(timeout)
          resolve()
        }
      })
    })
    expect(generatedBytes).toBeGreaterThan(1_048_576)
  } finally {
    if (writer.readyState !== WebSocket.CLOSED) {
      await new Promise<void>((resolve) => {
        writer.addEventListener('close', () => resolve(), { once: true })
        writer.close()
      })
    }
  }

  let browserReplayBytes = 0
  const browserHeaders: Array<{ screen?: boolean; interactive?: boolean }> = []
  const browserAttachAcks: Array<{
    ok?: boolean
    resumed?: boolean
    replay?: number
    resume_id?: string
  }> = []
  await page.routeWebSocket(/\/ws\/attach\//, (socket) => {
    const server = socket.connectToServer()
    socket.onMessage((message) => {
      if (typeof message === 'string') {
        try {
          const header = JSON.parse(message) as { screen?: boolean; interactive?: boolean }
          if (header.screen !== undefined || header.interactive !== undefined) browserHeaders.push(header)
        } catch {
          // Binary-output checks below are the proof; malformed client frames are
          // intentionally left to the real gateway.
        }
      }
      server.send(message)
    })
    server.onMessage((message) => {
      if (typeof message !== 'string') {
        browserReplayBytes += message.length
      } else {
        try {
          const ack = JSON.parse(message) as {
            ok?: boolean
            resumed?: boolean
            replay?: number
            resume_id?: string
          }
          if (ack.ok === true) browserAttachAcks.push(ack)
        } catch {
          // Non-JSON output is not an attach acknowledgement.
        }
      }
      socket.send(message)
    })
  })
  await page.setViewportSize({ width: 1568, height: 1000 })
  await page.goto(alice.url)
  await page
    .getByRole('complementary', { name: 'Runs' })
    .getByRole('button', { name: /snapshot current prompt/ })
    .click()

  const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
  await expect(rows).toContainText(currentPrompt, { timeout: 30_000 })
  await expect(rows).not.toContainText(firstOutput)
  await expect.poll(() => browserReplayBytes, { timeout: 30_000 }).toBeGreaterThan(0)
  expect(browserReplayBytes).toBeLessThan(512 * 1024)
  expect(browserHeaders[0]).toMatchObject({ screen: true, interactive: true })
  expect(browserAttachAcks[0]).toMatchObject({ ok: true })
  expect(browserAttachAcks[0]?.resumed).not.toBe(true)
  expect(browserAttachAcks[0]?.replay ?? 0).toBeLessThan(512 * 1024)
  await expect(page.getByRole('status', { name: 'Restoring terminal history' })).toBeHidden()
})
