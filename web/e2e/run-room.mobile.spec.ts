// Run details on a real phone viewport is a sheet over the live terminal,
// not a second narrow column. Opening and closing it must leave the PTY on the
// desktop viewer's grid.

import type { Member } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

const task = 'mobile release A run details'
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

test('a phone opens Run details as a sheet without resizing the run PTY', async ({
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
    const opener = page.getByRole('button', { name: 'Show details', exact: true })
    await opener.tap()
    const details = page.getByRole('dialog', { name: 'Run details' })
    await expect(details).toBeVisible()
    await expect.poll(async () => {
      const box = await details.boundingBox()
      return box && { x: box.x, width: box.width, bottom: Math.round(box.y + box.height) }
    }).toEqual({ x: 0, width: viewport!.width, bottom: viewport!.height })
    expect((await details.boundingBox())?.y).toBeGreaterThan(0)
    // The terminal remains underneath the sheet, and opening it did not make
    // this phone's width a new PTY geometry proposal.
    await expect(page.locator('.xterm:not([data-aether-frozen-view] *)')).toBeVisible()
    await expect(rows).toHaveCount(desktopRows)
    expect(await sessionGeometry()).toEqual(beforeOpen)
    await testInfo.attach('phone-run-details', {
      body: await page.screenshot(),
      contentType: 'image/png',
    })
    for (let step = 0; step < 6; step++) {
      await page.keyboard.press(step === 5 ? 'Shift+Tab' : 'Tab')
      await expect.poll(() => details.evaluate((element) => element.contains(document.activeElement))).toBe(true)
    }
    await page.keyboard.press('Escape')
    await expect(details).toBeHidden()
    await expect(opener).toBeFocused()

    // A tap cannot displace the same member's desktop session or open a
    // requester-side confirmation. A takeover requires a continuous hold.
    const presence = page.getByRole('group', { name: 'Run presence' })
    await expect(presence.getByText('You control in another tab')).toBeVisible()
    await presence.getByRole('button', { name: 'Take control' }).tap()
    await expect(page.getByRole('alertdialog')).toBeHidden()
    await expect(presence.getByRole('button', { name: 'Take control' })).toBeVisible()
    await expect(rows).toHaveCount(desktopRows)
    expect(await sessionGeometry()).toEqual(beforeOpen)
  } finally {
    desktop.close()
  }
})

test('a phone reads its captures in a full-width sheet', async ({
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
  await page.getByRole('button', { name: 'More', exact: true }).tap()
  await page.getByRole('menuitem', { name: 'Captures…' }).tap()
  const evidence = page.getByRole('dialog', { name: 'Captures', exact: true })
  const closeEvidence = evidence.getByRole('button', { name: 'Close', exact: true })
  await expect(evidence).toBeVisible()

  const viewport = page.viewportSize()
  expect(viewport).not.toBeNull()
  await expect.poll(async () => {
    const box = await evidence.boundingBox()
    return box && { x: box.x, width: box.width, bottom: Math.round(box.y + box.height) }
  }).toEqual({ x: 0, width: viewport!.width, bottom: viewport!.height })
  expect((await evidence.boundingBox())?.y).toBeGreaterThan(0)
  await expect(evidence.getByRole('button', { name: 'Open finish capture' })).toBeVisible()
  await testInfo.attach('phone-captures', {
    body: await page.screenshot(),
    contentType: 'image/png',
  })
  await closeEvidence.tap()
  await expect(evidence).toBeHidden()
})

test('incoming control decisions stay usable over Run details and Captures', async ({
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
    const requestPresence = requester.getByRole('group', { name: 'Run presence' })
    await expect(requestPresence.getByText(/^You control in another tab/)).toBeVisible()
    const takeControl = requestPresence.getByRole('button', { name: 'Take control', exact: true })
    const takeover = page.getByRole('alertdialog', { name: 'Run control requested' })
    const deny = takeover.getByRole('button', { name: 'Deny', exact: true })

    for (const { surface, width } of [
      { surface: 'Details', width: 390 },
      { surface: 'Captures', width: 390 },
      { surface: 'Captures', width: 700 },
    ] as const) {
      await page.setViewportSize({ width, height: 844 })
      await expect(page.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
      const room = surface === 'Details'
      if (room) await page.getByRole('button', { name: 'Show details', exact: true }).tap()
      else {
        await page.getByRole('button', { name: 'More', exact: true }).tap()
        await page.getByRole('menuitem', { name: 'Captures…' }).tap()
      }
      const panel = page.getByRole('dialog', { name: room ? 'Run details' : 'Captures', exact: true })
      await expect(panel).toBeVisible()
      if (room) await panel.getByRole('textbox', { name: 'Add a note' }).fill('Keep this interrupted draft')
      else await panel.getByRole('button', { name: 'Close', exact: true }).focus()

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
      await expect(requestPresence.getByRole('button', { name: 'Release', exact: true })).toBeHidden()

      if (room) {
        const note = panel.getByRole('textbox', { name: 'Add a note' })
        await expect(note).toHaveValue('Keep this interrupted draft')
        await expect(note).toBeFocused()
        await panel.getByRole('button', { name: 'Close', exact: true }).click()
      } else {
        const evidence = page.getByRole('dialog', { name: 'Captures', exact: true })
        const close = evidence.getByRole('button', { name: 'Close', exact: true })
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
