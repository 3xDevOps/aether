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

test('terminal history finds early output omitted from the live viewport', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const firstOutput = 'EARLY-OUTPUT-ONLY-IN-DOWNLOAD'
  const currentOutput = 'CURRENT-VIEWPORT-OUTPUT'
  const redrawFill = 'z'.repeat(96)
  aether.installAgent(
    await memberID(alice),
    'claude',
    `stty -echo
IFS= read -r start
printf '\\033[2J\\033[H${firstOutput}\\r\\n'
i=0
while [ "$i" -lt 12000 ]; do
  printf '\\033[2J\\033[HOLD-%05d-${redrawFill}\\r\\n' "$i"
  i=$((i + 1))
done
printf '\\033[2J\\033[H${currentOutput}\\r\\n'
sleep 600
`,
  )
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'page complete terminal history',
  })
  const url = new URL(alice.url)
  const writer = await openWriter(url, run.id, 'history-writer')
  await sendInputUntil(writer, 'go\r', currentOutput)
  await releaseWriter(writer, 1)
  await closeWriter(writer.socket)

  await page.goto(alice.url)
  await page
    .getByRole('complementary', { name: 'Runs' })
    .getByRole('button', { name: /page complete terminal history/ })
    .click()
  const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
  await expect(rows).toContainText(currentOutput, { timeout: 30_000 })
  await expect(rows).not.toContainText(firstOutput)
  await page.getByRole('button', { name: 'Open terminal history', exact: true }).click()
  await page.getByRole('searchbox', { name: 'Search terminal history' }).fill(firstOutput)
  const history = page.getByLabel('Terminal history output')
  for (let attempt = 0; attempt < 8; attempt++) {
    try {
      await expect(history).toContainText(firstOutput, { timeout: 5_000 })
      return
    } catch (error) {
      const older = page.getByRole('button', { name: 'Load older history', exact: true })
      if ((await older.count()) === 0) throw error
      await older.click()
    }
  }
  await expect(history).toContainText(firstOutput)
})
