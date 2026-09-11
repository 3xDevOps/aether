// What a phone may and may not do to a run's terminal. The gateway's attach
// socket is served here rather than proxied, because the claim under test is
// about the frames the phone sends: the PTY is the per-dimension minimum over
// write-capable clients, so a client that never sends a resize can never
// shrink the agent's screen for the people watching it on a desktop.

import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, shrinkToKeyboardHeight, test } from './mobile'

/** A desktop viewer's geometry, as the session reports it in every ack. */
const serverCols = 132
const serverRows = 43

interface Attach {
  header: Record<string, unknown> | null
  controls: Record<string, unknown>[]
}

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a phone watches a run at the server geometry and resizes nothing', async ({
  page,
  aether,
}) => {
  const attaches: Attach[] = []
  await page.routeWebSocket(/\/ws\/attach\//, (ws) => {
    const attach: Attach = { header: null, controls: [] }
    attaches.push(attach)
    ws.onMessage((message) => {
      const frame = JSON.parse(String(message)) as Record<string, unknown>
      if (attach.header === null) {
        attach.header = frame
        ws.send(JSON.stringify({ ok: true, cols: serverCols, rows: serverRows }))
        return
      }
      attach.controls.push(frame)
    })
  })

  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  // An agent that outlives the test: the run has to stay steerable, and the
  // seed repository's own script exits at once.
  aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  await alice.api.rpc('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'a run watched from a phone',
  })

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
  expect(attaches[0].header).toEqual({ cols: 80, rows: 24 })
  await expect(page.getByRole('toolbar', { name: 'Terminal keys' })).toBeHidden()

  // The grid is the session's, so it is wider than the phone and the pane
  // pans over it rather than reflowing the agent's screen to fit.
  const rows = page.locator('.xterm-rows > div')
  await expect(rows).toHaveCount(serverRows)
  const overflow = await page.locator('.xterm').evaluate((el) => {
    const host = el.parentElement
    return host ? host.scrollWidth - host.clientWidth : 0
  })
  expect(overflow).toBeGreaterThan(0)

  await page.getByRole('button', { name: 'Take control' }).tap()
  await expect(page.getByRole('button', { name: 'Steering' })).toBeVisible()
  expect(attaches[1].header).toEqual({ write: true, cols: 80, rows: 24 })
  await expect(rows).toHaveCount(serverRows)

  // Esc is the key an agent TUI needs most and the one no phone keyboard
  // has. It reaches the shell as the byte a keyboard would have sent.
  await page
    .getByRole('toolbar', { name: 'Terminal keys' })
    .getByRole('button', { name: 'Esc' })
    .tap()
  await expect
    .poll(() => attaches[1].controls)
    .toContainEqual({ type: 'input', data: '\u001b' })

  // The soft keyboard shortens the layout, which is what would make a fitted
  // terminal re-fit and send its new size.
  const restore = await shrinkToKeyboardHeight(page)
  await expect(rows).toHaveCount(serverRows)
  await restore()
  await expect(rows).toHaveCount(serverRows)

  const resizes = attaches.flatMap((attach) =>
    attach.controls.filter((frame) => frame.type === 'resize'),
  )
  expect(resizes).toEqual([])
})
