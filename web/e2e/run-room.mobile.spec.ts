// The Run Room on a real phone viewport is a sheet over the live terminal,
// not a second narrow column. Opening and closing it must leave the PTY on the
// desktop viewer's grid.

import type { Member } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

const task = 'mobile release A run room'
const desktopCols = 132
const desktopRows = 43
const desktopSessionID = 'run-room-mobile-desktop'
const probeSessionID = 'run-room-mobile-probe'

interface AttachAck {
  ok: boolean
  cols: number
  rows: number
  error?: string
}

/** A real raw desktop attach that establishes the PTY geometry. */
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
    let settled = false
    const fail = (why: string) => {
      if (settled) return
      settled = true
      reject(new Error(`attach ${runID}: ${why}`))
    }
    socket.addEventListener('error', () => fail('socket error'))
    socket.addEventListener('close', (event) => fail(`closed: ${event.reason || event.code}`))
    socket.addEventListener('open', () => socket.send(JSON.stringify(header)))
    socket.addEventListener('message', (event) => {
      if (typeof event.data !== 'string' || settled) return
      settled = true
      resolve(JSON.parse(event.data) as AttachAck)
    })
  })
  return { ack, close: () => socket.close() }
}

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a phone opens Run Room as a full sheet without resizing the run PTY', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('Alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task,
  })

  const desktop = await attach(alice, run.id, {
    write: true,
    cols: desktopCols,
    rows: desktopRows,
    control_session_id: desktopSessionID,
  })
  try {
    expect(desktop.ack.ok).toBe(true)
    const sessionGeometry = async () => {
      const probe = await attach(alice, run.id, {
        follow: true,
        control_session_id: probeSessionID,
      })
      probe.close()
      return { cols: probe.ack.cols, rows: probe.ack.rows }
    }
    expect(await sessionGeometry()).toEqual({ cols: desktopCols, rows: desktopRows })

    await page.goto(`${alice.url}&run=${run.id}`)
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
    await expect(page.getByText('Attached', { exact: true })).toBeVisible()
    const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *) > div')
    await expect(rows).toHaveCount(desktopRows)

    const viewport = page.viewportSize()
    expect(viewport).not.toBeNull()
    const beforeOpen = await sessionGeometry()
    await page.getByRole('button', { name: 'Open Run Room' }).tap()
    const room = page.getByRole('complementary', { name: 'Run Room' })
    await expect(room).toBeVisible()
    await expect(room).toHaveClass(/fixed inset-x-0/)
    await expect(room).toHaveClass(/top-\[calc\(var\(--title-bar-height\)\+var\(--safe-top\)\)\]/)
    const box = await room.boundingBox()
    expect(box).not.toBeNull()
    expect(box?.x).toBe(0)
    expect(box?.y).toBeGreaterThan(0)
    expect(box?.width).toBe(viewport?.width)
    expect(Math.round((box?.y ?? 0) + (box?.height ?? 0))).toBe(viewport?.height)
    // The terminal remains underneath the sheet, and opening the room did not
    // make this phone's width a new PTY geometry proposal.
    await expect(page.locator('.xterm:not([data-aether-frozen-view] *)')).toBeVisible()
    await expect(rows).toHaveCount(desktopRows)
    expect(await sessionGeometry()).toEqual(beforeOpen)

    // The controller is this same member in a different session. The modal
    // must remain visible and tappable above the full-screen room sheet.
    await room.getByRole('button', { name: 'Take control' }).tap()
    const takeover = page.getByRole('dialog', { name: 'Take control of this run?' })
    await expect(takeover).toBeVisible()
    await takeover.getByRole('button', { name: 'Cancel' }).tap()
    await expect(takeover).toBeHidden()

    await page.getByRole('button', { name: 'Close Run Room' }).tap()
    await expect(room).toBeHidden()
    await expect(page.getByRole('button', { name: 'Open Run Room' })).toBeVisible()
    await expect(page.getByText('Attached', { exact: true })).toBeVisible()
    await expect(rows).toHaveCount(desktopRows)
    expect(await sessionGeometry()).toEqual(beforeOpen)
  } finally {
    desktop.close()
  }
})

test('a phone reads retained evidence in a full-width surface', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('Alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task: 'mobile retained evidence',
    mode: 'headless',
  })

  await expect
    .poll(
      async () => {
        const { packets } = await alice.api.rpc<{ packets: unknown[] }>(
          'run.evidence.list',
          {
            workspace_id: workspaces[0].id,
            run_id: run.id,
            limit: 50,
          },
        )
        return packets.length
      },
      { timeout: 3 * 60 * 1000, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBeGreaterThan(0)

  await page.goto(`${alice.url}&run=${run.id}`)
  await page.getByRole('button', { name: 'Open Run Room' }).tap()
  const evidence = page.getByRole('region', { name: 'Run evidence' })
  await evidence.getByRole('button', { name: /^Evidence/ }).tap()
  const closeEvidence = evidence.getByRole('button', { name: 'Close evidence' })
  const surface = closeEvidence.locator('xpath=../..')
  await expect(surface).toBeVisible()

  const viewport = page.viewportSize()
  const box = await surface.boundingBox()
  expect(viewport).not.toBeNull()
  expect(box).not.toBeNull()
  expect(box?.x).toBe(0)
  expect(box?.y).toBeGreaterThan(0)
  expect(box?.width).toBe(viewport?.width)
  expect(Math.round((box?.y ?? 0) + (box?.height ?? 0))).toBe(viewport?.height)
  await expect(evidence.getByRole('button', { name: /^finish capture/ })).toBeVisible()
  await expect(evidence.getByRole('heading', { name: 'Retained evidence' })).toBeVisible()
  await closeEvidence.tap()
  await expect(surface).toBeHidden()
})
