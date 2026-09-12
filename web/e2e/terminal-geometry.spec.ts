import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// Cursor-addressed output exposes a wrong grid where plain echo does not.
const painter = `stty -echo
paint() {
  set -- $(stty size)
  printf '\\033[2J\\033[H'
  printf '\\033[%s;%sHX' "$1" "$2"
}
trap paint WINCH
paint
while :; do
  if IFS= read -r line; then paint; fi
done
`

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
  try {
    await new Promise<void>((resolve, reject) => {
      writer.addEventListener('error', () => reject(new Error('writer socket failed')))
      writer.addEventListener('open', () => writer.send(JSON.stringify({ write: true, cols: 60, rows: 18 })))
      writer.addEventListener('message', (event) => {
        if (typeof event.data !== 'string') return
        const ack = JSON.parse(event.data)
        if (!ack.ok) reject(new Error(ack.error ?? 'writer refused'))
        else resolve()
      }, { once: true })
    })
    await page.setViewportSize({ width: 1568, height: 1000 })
    await page.goto(alice.url)
    await page.getByRole('complementary', { name: 'Runs' }).getByRole('button', { name: /shared geometry regression/ }).click()
    const rows = page.locator('.xterm-rows > div')
    const assertGrid = async (cols: number, height: number) => {
      await expect(rows).toHaveCount(height)
      writer.send(JSON.stringify({ type: 'input', data: '\r' }))
      await expect(rows.nth(height - 1)).toHaveText(`${' '.repeat(cols - 1)}X`)
    }
    await assertGrid(60, 18)
    writer.send(JSON.stringify({ type: 'resize', cols: 72, rows: 22 }))
    await assertGrid(72, 22)
    // The larger viewer must keep proposing its pane, not feed 60x18 back
    // into the minimum and stop the shared terminal ever growing again.
    await page.getByRole('button', { name: 'Increase terminal text size' }).click()
    await assertGrid(72, 22)
    await page.getByRole('button', { name: 'Steering', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Take control' })).toBeVisible()
    await assertGrid(72, 22)
    await page.getByRole('tab', { name: 'Overview', exact: true }).click()
    await page.getByRole('tab', { name: 'Terminal', exact: true }).click()
    await assertGrid(72, 22)
  } finally {
    writer.close()
  }
})

test('fresh runs show the current prompt while History keeps the complete recording', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  const firstOutput = 'HISTORY-FIRST-OUTPUT'
  const currentPrompt = 'CURRENT-SNAPSHOT-PROMPT> '
  const confirmation = 'SNAPSHOT-LIVE-CONFIRMATION'
  // Each iteration clears and redraws the screen. The 96-byte payload makes
  // the old redraws exceed the one-MiB live replay ring before the prompt.
  const redrawFill = 'x'.repeat(96)
  // Emit the first screen only after the server has attached its recorder.
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
  printf '\\r\\n${confirmation}:%s\\r\\n${currentPrompt}' "$line"
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
      const finish = () => {
        if (settled) return
        settled = true
        clearTimeout(timeout)
        resolve()
      }
      timeout = setTimeout(() => fail(new Error('agent did not finish its redraws')), 180_000)

      writer.addEventListener('error', () => fail(new Error('writer socket failed')))
      writer.addEventListener('close', () => {
        if (!settled) fail(new Error('writer socket closed before the prompt'))
      })
      writer.addEventListener('open', () => {
        writer.send(JSON.stringify({ write: true, cols: 80, rows: 24 }))
      })
      writer.addEventListener('message', (event) => {
        if (typeof event.data === 'string') {
          let ack: { ok?: boolean; error?: string }
          try {
            ack = JSON.parse(event.data) as { ok?: boolean; error?: string }
          } catch {
            return
          }
          if (ack.ok === undefined) return
          if (!ack.ok) {
            fail(new Error(ack.error ?? 'writer refused'))
          } else if (!started) {
            started = true
            writer.send(JSON.stringify({ type: 'input', data: 'go\r' }))
          }
          return
        }

        const chunk = new Uint8Array(event.data as ArrayBuffer)
        generatedBytes += chunk.byteLength
        text = (text + new TextDecoder().decode(chunk)).slice(-8192)
        if (text.includes(currentPrompt)) finish()
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
  await page.routeWebSocket(/\/ws\/attach\//, (socket) => {
    const server = socket.connectToServer()
    server.onMessage((message) => {
      if (typeof message !== 'string') browserReplayBytes += message.length
      socket.send(message)
    })
  })
  await page.setViewportSize({ width: 1568, height: 1000 })
  await page.goto(alice.url)
  await page
    .getByRole('complementary', { name: 'Runs' })
    .getByRole('button', { name: /snapshot current prompt/ })
    .click()

  const rows = page.locator('.xterm-rows')
  await expect(rows).toContainText(currentPrompt, { timeout: 30_000 })
  await expect(rows).not.toContainText(firstOutput)
  await expect.poll(() => browserReplayBytes, { timeout: 10_000 }).toBeGreaterThan(0)
  // The fresh framed attach carries a compact screen, not the old redraw
  // transcript. Leave a generous margin over a 200-line, 80-column screen.
  expect(browserReplayBytes).toBeLessThan(512 * 1024)

  const screen = page.locator('.xterm-screen')
  await screen.click()
  const sentAt = Date.now()
  await page.keyboard.type('snapshot-input-check')
  await page.keyboard.press('Enter')
  await expect(rows).toContainText(`${confirmation}:snapshot-input-check`, {
    timeout: 15_000,
  })
  expect(Date.now() - sentAt).toBeLessThan(15_000)
  await expect(page.getByRole('button', { name: 'Steering', exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Open complete terminal history' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  const historyTerminal = dialog.locator('.ap-term-text')
  await expect(dialog.getByRole('button', { name: 'Play', exact: true })).toBeVisible({
    timeout: 30_000,
  })
  await dialog.getByRole('button', { name: 'Play', exact: true }).click()
  await expect(historyTerminal).toContainText(firstOutput, { timeout: 30_000 })
  await dialog.getByRole('button', { name: 'Beginning', exact: true }).click()
  await expect(historyTerminal).toContainText(firstOutput, { timeout: 30_000 })

  await dialog.getByRole('button', { name: 'Back to live', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(rows).toContainText(currentPrompt)
  await expect(page.getByRole('button', { name: 'Steering', exact: true })).toBeVisible()
})
