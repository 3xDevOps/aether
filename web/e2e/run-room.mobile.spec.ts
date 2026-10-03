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
}, testInfo) => {
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
    const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *) > div')
    await expect(rows).toHaveCount(desktopRows)

    const viewport = page.viewportSize()
    expect(viewport).not.toBeNull()
    const beforeOpen = await sessionGeometry()
    // The room makes the background inaccessible while its modal is open.
    const titlebar = await page.getByRole('banner', { name: 'Aether' }).boundingBox()
    expect(titlebar).not.toBeNull()
    await page.getByRole('button', { name: 'Open Run Room' }).tap()
    const room = page.getByRole('dialog', { name: 'Run Room' })
    await expect(room).toBeVisible()
    const box = await room.boundingBox()
    expect(box).not.toBeNull()
    expect(box?.y).toBe((titlebar?.y ?? 0) + (titlebar?.height ?? 0))
    expect(box?.x).toBe(0)
    expect(box?.y).toBeGreaterThan(0)
    expect(box?.width).toBe(viewport?.width)
    expect(Math.round((box?.y ?? 0) + (box?.height ?? 0))).toBe(viewport?.height)
    // The terminal remains underneath the sheet, and opening the room did not
    // make this phone's width a new PTY geometry proposal.
    await expect(page.locator('.xterm:not([data-aether-frozen-view] *)')).toBeVisible()
    await expect(rows).toHaveCount(desktopRows)
    expect(await sessionGeometry()).toEqual(beforeOpen)
    await testInfo.attach('phone-run-room', {
      body: await page.screenshot(),
      contentType: 'image/png',
    })

    const composer = room.getByRole('textbox', { name: 'Run Room message' })
    await composer.fill('Keep this phone draft')
    for (let step = 0; step < 12; step++) {
      await page.keyboard.press(step === 11 ? 'Shift+Tab' : 'Tab')
      await expect.poll(() => room.evaluate((element) => element.contains(document.activeElement))).toBe(true)
    }
    await page.keyboard.press('Escape')
    await expect(room).toBeHidden()
    await expect(page.getByRole('button', { name: 'Open Run Room' })).toBeFocused()
    await page.getByRole('button', { name: 'Open Run Room' }).tap()
    await expect(composer).toHaveValue('Keep this phone draft')

    // A tap cannot displace the same member's desktop session or open a
    // requester-side confirmation. A takeover requires a continuous hold.
    await expect(room.getByText(/Controller: /)).toBeVisible()
    await room.getByRole('button', { name: 'Take control' }).tap()
    await expect(page.getByRole('alertdialog')).toBeHidden()
    await expect(room.getByRole('button', { name: 'Take control' })).toBeVisible()
    expect(await sessionGeometry()).toEqual(beforeOpen)

    await page.getByRole('button', { name: 'Close Run Room' }).tap()
    await expect(room).toBeHidden()
    await expect(page.getByRole('button', { name: 'Open Run Room' })).toBeVisible()
    await expect(rows).toHaveCount(desktopRows)
    expect(await sessionGeometry()).toEqual(beforeOpen)
  } finally {
    desktop.close()
  }
})

test('a phone reads retained evidence in a full-width surface', async ({
  page,
  aether,
}, testInfo) => {
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
  await page.getByRole('button', { name: /^Evidence(?: \(\d+\))?$/ }).tap()
  const evidence = page.getByRole('dialog', { name: 'Retained evidence', exact: true })
  const closeEvidence = evidence.getByRole('button', { name: 'Close evidence', exact: true })
  await expect(evidence).toBeVisible()

  const viewport = page.viewportSize()
  const box = await evidence.boundingBox()
  expect(viewport).not.toBeNull()
  expect(box).not.toBeNull()
  expect(box?.x).toBe(0)
  expect(box?.y).toBeGreaterThan(0)
  expect(box?.width).toBe(viewport?.width)
  expect(Math.round((box?.y ?? 0) + (box?.height ?? 0))).toBe(viewport?.height)
  await expect(evidence.getByRole('button', { name: /^finish capture/ })).toBeVisible()
  await expect(evidence.getByRole('heading', { name: 'Retained evidence' })).toBeVisible()
  await testInfo.attach('phone-retained-evidence', {
    body: await page.screenshot(),
    contentType: 'image/png',
  })
  await closeEvidence.tap()
  await expect(evidence).toBeHidden()
})

test('incoming control decisions stay usable over responsive Room and Evidence', async ({
  page,
  aether,
}, testInfo) => {
  const alice = await aether.member('Alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'phone holder decision',
  })
  const phone = { width: 390, height: 844 }
  await page.setViewportSize(phone)
  await page.goto(`${alice.url}&run=${run.id}`)
  await page.getByRole('button', { name: 'Take control', exact: true }).tap()
  await expect(page.getByRole('button', { name: 'Release', exact: true })).toBeVisible()

  const requester = await page.context().newPage()
  try {
    await requester.goto(`${alice.url}&run=${run.id}`)
    await requester.getByRole('button', { name: 'Open Run Room' }).tap()
    const requestRoom = requester.getByRole('dialog', { name: 'Run Room' })
    await expect(requestRoom.getByText('Controller: Alice', { exact: true })).toBeVisible()
    const takeControl = requestRoom.getByRole('button', { name: 'Take control', exact: true })
    const takeover = page.getByRole('alertdialog', { name: 'Terminal control requested' })
    const deny = takeover.getByRole('button', { name: 'Deny', exact: true })

    for (const { surface, width } of [
      { surface: 'Room', width: 390 },
      { surface: 'Evidence', width: 390 },
      { surface: 'Evidence', width: 700 },
    ] as const) {
      await page.setViewportSize({ width, height: 844 })
      await expect(page.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
      const room = surface === 'Room'
      await page.getByRole('button', { name: room ? 'Open Run Room' : /^Evidence(?: \(\d+\))?$/ }).tap()
      const panel = page.getByRole('dialog', { name: room ? 'Run Room' : 'Retained evidence', exact: true })
      await expect(panel).toBeVisible()
      if (room) await panel.getByRole('textbox', { name: 'Run Room message' }).fill('Keep this interrupted draft')
      else await panel.getByRole('button', { name: 'Close evidence', exact: true }).focus()

      await expect(takeControl).toHaveAttribute('aria-disabled', 'false')
      await takeControl.focus()
      await requester.keyboard.down('Space')
      try {
        await expect(takeover).toBeVisible({ timeout: 15_000 })
      } finally {
        await requester.keyboard.up('Space')
      }
      await expect(deny).toBeFocused()
      expect(await takeover.evaluate((dialog) => {
        const layer = Number(getComputedStyle(dialog).zIndex)
        return Array.from(document.querySelectorAll('[role="dialog"]')).every(
          (background) => layer > Number(getComputedStyle(background).zIndex),
        )
      })).toBe(true)
      await testInfo.attach(`holder-over-${surface.toLowerCase()}-${width}`, {
        body: await page.screenshot(),
        contentType: 'image/png',
      })

      await page.setViewportSize({ width: width === 700 ? 960 : width, height: width === 700 ? 844 : 524 })
      await expect(deny).toBeFocused()
      await page.keyboard.press('Tab')
      await expect(takeover.getByRole('button', { name: 'Accept', exact: true })).toBeFocused()
      await page.keyboard.press('Shift+Tab')
      await expect(deny).toBeFocused()
      expect(await deny.evaluate((button) => {
        const box = button.getBoundingClientRect()
        return button.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2))
      })).toBe(true)
      await deny.click()
      await expect(takeover).toBeHidden()
      await expect(takeControl).toBeVisible()
      await expect(requestRoom.getByRole('button', { name: 'Release', exact: true })).toBeHidden()

      if (room) {
        const composer = panel.getByRole('textbox', { name: 'Run Room message' })
        await expect(composer).toHaveValue('Keep this interrupted draft')
        await expect(composer).toBeFocused()
        await panel.getByRole('button', { name: 'Close Run Room' }).click()
      } else {
        const evidence = page.getByRole('dialog', { name: 'Retained evidence', exact: true })
        const close = evidence.getByRole('button', { name: 'Close evidence', exact: true })
        await expect(close).toBeFocused()
        const box = await evidence.boundingBox()
        expect(box).not.toBeNull()
        if (width === 700) expect(box!.width).toBeLessThan(page.viewportSize()!.width)
        else expect(box!.width).toBe(width)
        await close.click()
      }
      await expect(page.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
      await page.setViewportSize(phone)
    }
  } finally {
    await requester.close()
  }
})
