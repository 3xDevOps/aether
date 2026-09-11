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

interface AttachAck {
  ok: boolean
  cols: number
  rows: number
  error?: string
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
): Promise<{ ack: AttachAck; close: () => void }> {
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
  return { ack, close: () => socket.close() }
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
  })
  expect(desktop.ack.ok).toBe(true)
  // What the session is now, asked of the server rather than inferred: a
  // fresh follow attach is answered with the live PTY geometry.
  const sessionGeometry = async () => {
    const probe = await attach(alice, run.id, { follow: true })
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
  const rows = page.locator('.xterm-rows > div')
  await expect(rows).toHaveCount(desktopRows)
  const grid = () =>
    page.locator('.xterm').evaluate((el) => {
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
  expect(await grid()).toEqual({ cols: desktopCols, pannable: true })

  // Taking control is the whole point: a phone types into the agent without
  // the agent's screen being resized to fit the phone.
  await page.getByRole('button', { name: 'Take control' }).tap()
  await expect(page.getByRole('button', { name: 'Steering' })).toBeVisible()
  await expect(page.getByRole('toolbar', { name: 'Terminal keys' })).toBeVisible()
  expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })
  await expect(rows).toHaveCount(desktopRows)

  // Esc is the key an agent TUI needs most and the one no phone keyboard
  // has. It reaches the agent through the same path a keystroke takes.
  await page
    .getByRole('toolbar', { name: 'Terminal keys' })
    .getByRole('button', { name: 'Esc' })
    .tap()

  // The soft keyboard shortens the layout, which is what would make a
  // terminal that fitted its pane re-fit and resize the session with it.
  const restore = await shrinkToKeyboardHeight(page)
  await expect(rows).toHaveCount(desktopRows)
  expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })
  await restore()

  // The other way round: the desktop viewer's window changes, and the phone
  // follows it there without reattaching.
  desktop.close()
  const narrower = await attach(alice, run.id, { write: true, cols: 100, rows: 30 })
  expect(narrower.ack.ok).toBe(true)
  await expect(rows).toHaveCount(30)
  expect(await grid()).toEqual({ cols: 100, pannable: true })
  narrower.close()
})
