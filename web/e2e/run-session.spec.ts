// An Enhanced run's Session view against a real server: the acpmock agent
// runs as the run's ACP adapter, the dashboard streams /ws/acp, answers a
// permission natively, sends a message, queues one behind a turn and interrupts it.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('an Enhanced run streams, answers a permission, takes a message and stops on Interrupt', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'mock',
    mode: 'acp',
    task: 'demo',
  })

  await page.goto(`${alice.url}&run=${run.id}`)
  const log = page.getByRole('log', { name: 'Session' })
  await expect(log.getByText('pong')).toBeVisible({ timeout: 60_000 })
  const box = page.getByRole('combobox', { name: 'Message the agent' })

  await box.fill('ask permission')
  await box.press('ControlOrMeta+Enter')
  const card = page.locator('#session-request-docked')
  await expect(card.getByText('rm -rf build')).toBeVisible()
  await expect(page.getByText('Answer the request above to continue.')).toBeVisible()
  await page.getByRole('toolbar', { name: 'Run actions' }).getByRole('button', { name: 'Answer' }).click()
  await expect(card).toBeFocused()
  await expect(card.getByRole('button')).toHaveText(['Allow', 'Reject', 'Always Allow'])
  await expect(page.getByRole('log', { name: 'Session' }).getByText('Waiting for your approval: rm -rf build')).toBeVisible()
  await page.keyboard.press('1')
  await expect(log.getByText(/Approved: run/)).toBeVisible()
  await expect(log.getByText('permission: allow')).toBeVisible()
  await expect.poll(async () => {
    const [row, view] = await Promise.all([log.getByText('permission: allow').boundingBox(), log.boundingBox()])
    return Boolean(row && view && row.y >= view.y && row.y + row.height <= view.y + view.height)
  }, { message: 'the answered reply sits inside the scrolled log' }).toBe(true)

  await box.fill('ask permission')
  await box.press('ControlOrMeta+Enter')
  await expect(card.getByText('rm -rf build')).toBeVisible()
  await expect(card).not.toBeFocused()
  await card.getByRole('button', { name: 'Allow', exact: true }).click()
  await expect(log.getByText('permission: allow', { exact: true })).toHaveCount(2)
  await page.screenshot({ path: testInfo.outputPath('enhanced-first-click-approval.png') })

  await box.fill('demo')
  await box.press('ControlOrMeta+Enter')
  await expect(log.getByRole('button', { name: /Read 2 files, ran 1 command and edited 1 file/ })).toBeVisible({ timeout: 30_000 })
  await expect(log.getByText('Changed 1 file')).toBeVisible()
  await log.getByRole('button', { name: /Read 2 files, ran 1 command and edited 1 file/ }).click()
  await log.getByRole('button', { name: /Edited src\/billing\.js/ }).click()
  await expect(log.locator('[data-slot="diff-block"]')).toContainText('Math.round')
  // The mock advertises steering but does not implement it, so wait for the turn to end.
  await expect(log.getByText(/^Finished in \d+s$/)).toBeVisible()

  await box.fill('wait')
  await box.press('ControlOrMeta+Enter')
  const interrupt = page.getByRole('button', { name: 'Interrupt the agent' })
  await expect(interrupt).toBeVisible()
  await box.fill('after the wait')
  await box.press('ControlOrMeta+Shift+Enter')
  const queued = log.locator('[data-slot="message-row"]', { hasText: 'after the wait' })
  await expect(queued).toContainText('Queued')
  await interrupt.click()
  await expect(log.getByText(/^Interrupted/)).toBeVisible()
  await expect(queued).not.toContainText('Queued')
  await expect.poll(async () => {
    const { messages } = await alice.api.rpc<{ messages: { body: string; state: string; agent_delivery?: string }[] }>(
      'run.room.list', { workspace_id: workspaces[0].id, run_id: run.id },
    )
    const sent = messages.find((m) => m.body === 'after the wait')
    return sent && `${sent.state}/${sent.agent_delivery}`
  }, { message: 'the queued message is recorded as delivered' }).toBe('sent/delivered')
  await expect(page.getByRole('button', { name: 'Send' })).toBeVisible()
})

test('Enhanced multiplayer keeps moderated messages, deliberate release and timed takeover', async ({ page, browser, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('Alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const bob = await aether.member('Bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite: await aether.invite(alice), name: 'Bob' })
  const { member: aliceMember } = await alice.api.rpc<{ member: { display_name: string } }>('server.info')
  const { member: bobMember } = await bob.api.rpc<{ member: { display_name: string } }>('server.info')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id, harness: 'mock', mode: 'acp', task: 'demo',
  })
  const bobContext = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const bobPage = await bobContext.newPage()
  try {
    await page.goto(`${alice.url}&run=${run.id}`)
    await expect(page.getByRole('log', { name: 'Session' }).getByText('pong')).toBeVisible({ timeout: 60_000 })
    const aliceControls = page.getByRole('group', { name: 'Multiplayer controls' })
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await bobPage.goto(`${bob.url}&run=${run.id}`)
    const bobControls = bobPage.getByRole('group', { name: 'Multiplayer controls' })
    const takeControl = bobControls.getByRole('button', { name: 'Take control', exact: true })
    await expect(bobControls).toContainText(`${aliceMember.display_name} controls`)
    await expect(takeControl).toHaveAttribute('aria-disabled', 'false')

    for (const controls of [aliceControls, bobControls]) {
      await controls.getByRole('button', { name: 'Multiplayer', exact: true }).click()
    }
    const aliceDetails = page.getByRole('complementary', { name: 'Run details' })
    const bobDetails = bobPage.getByRole('complementary', { name: 'Run details' })
    const note = 'I will check the migration while you work.'
    await bobDetails.getByRole('textbox', { name: 'Add a note' }).fill(note)
    await bobDetails.getByRole('button', { name: 'Add', exact: true }).click()
    await expect(aliceDetails.getByRole('region', { name: 'Notes' })).toContainText(note)

    const instruction = 'Please inspect the migration'
    const composer = bobPage.getByRole('combobox', { name: 'Message the agent' })
    await expect(bobPage.getByText('Delivers in 45 s unless the controller decides sooner.')).toBeVisible()
    await composer.fill(instruction)
    await bobPage.getByRole('button', { name: 'Send', exact: true }).click()
    const request = aliceDetails.getByRole('region', { name: 'Needs you' }).locator('[data-slot=request-card]').filter({ hasText: instruction })
    await expect(request).toContainText(`${bobMember.display_name} sent the agent a message`)
    await expect(request.getByRole('button', { name: 'Deny', exact: true })).toBeVisible()
    await request.getByRole('button', { name: 'Approve', exact: true }).click()
    await expect.poll(async () => {
      const { messages } = await alice.api.rpc<{ messages: { body: string; state: string }[] }>('run.room.list', {
        workspace_id: workspaces[0].id, run_id: run.id,
      })
      return messages.find((message) => message.body === instruction)?.state
    }).toBe('sent')
    await expect(page.getByRole('log', { name: 'Session' }).getByText('pong', { exact: true })).toHaveCount(2)

    const ownerComposer = page.getByRole('combobox', { name: 'Message the agent' })
    await ownerComposer.fill('ask permission')
    await ownerComposer.press('ControlOrMeta+Enter')
    const permission = page.locator('#session-request-docked')
    await expect(permission.getByRole('button', { name: 'Allow', exact: true })).toBeVisible()
    await expect(bobPage.locator('#session-request-docked').getByRole('button', { name: 'Allow', exact: true })).toBeDisabled()
    const pendingInstruction = 'Please check rollback after approval'
    await composer.fill(pendingInstruction)
    await bobPage.getByRole('button', { name: 'Send', exact: true }).click()
    const pendingSteer = aliceDetails.getByRole('region', { name: 'Needs you' }).locator('[data-slot=request-card]').filter({ hasText: pendingInstruction })
    await pendingSteer.getByRole('button', { name: 'Approve', exact: true }).click()
    await expect.poll(async () => {
      const { messages } = await alice.api.rpc<{ messages: { body: string; agent_delivery?: string }[] }>('run.room.list', {
        workspace_id: workspaces[0].id, run_id: run.id,
      })
      return messages.find((message) => message.body === pendingInstruction)?.agent_delivery
    }).toBe('queued')
    await bobPage.screenshot({ path: testInfo.outputPath('enhanced-multiplayer-pending.png') })
    await permission.getByRole('button', { name: 'Allow', exact: true }).click()
    await expect.poll(async () => {
      const { messages } = await alice.api.rpc<{ messages: { body: string; agent_delivery?: string }[] }>('run.room.list', {
        workspace_id: workspaces[0].id, run_id: run.id,
      })
      return messages.find((message) => message.body === pendingInstruction)?.agent_delivery
    }).toBe('delivered')

    await takeControl.click()
    await expect(bobControls.getByRole('alert')).toContainText('run control is held by another session')
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    const takeover = page.getByRole('alertdialog', { name: 'Run control requested' })
    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    await expect(takeControl).toContainText(/Take control · [1-5]s/)
    await bobPage.keyboard.up('Space')
    await expect(takeControl).toHaveText('Take control')
    await expect(takeover).toBeHidden()

    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await expect(takeover).toContainText(bobMember.display_name)
    await takeover.getByRole('button', { name: 'Deny', exact: true }).click()
    await expect(takeover).toBeHidden()
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(takeControl).toHaveAttribute('aria-disabled', 'false')

    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await expect(takeover.getByRole('status')).toContainText('Control transfers automatically')
    await page.screenshot({ path: testInfo.outputPath('enhanced-takeover-review.png') })
    await expect(bobControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible({ timeout: 15_000 })
    await expect(aliceControls).toContainText(`${bobMember.display_name} controls`)
    await expect(aliceControls.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()
    await bobControls.getByRole('button', { name: 'Release', exact: true }).click()
    await expect(bobControls).toContainText('Nobody controls')
    await expect(aliceControls).toContainText('Nobody controls')

    await page.getByRole('tab', { name: 'Terminal', exact: true }).click()
    await expect(page.getByText('No agent terminal')).toBeVisible()
    await aliceControls.getByRole('button', { name: 'Take control', exact: true }).click()
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await aliceControls.getByRole('button', { name: 'Release', exact: true }).click()
    await expect(aliceControls).toContainText('Nobody controls')
    await page.getByRole('tab', { name: 'Session', exact: true }).click()
    await expect(aliceControls.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()

    await bobPage.setViewportSize({ width: 390, height: 844 })
    await expect(bobControls.getByRole('button', { name: 'Multiplayer', exact: true })).toBeVisible()
    await expect(takeControl).toBeVisible()
    const bounds = await bobControls.boundingBox()
    expect(bounds).not.toBeNull()
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390)
    await bobPage.screenshot({ path: testInfo.outputPath('enhanced-multiplayer-phone.png') })
  } finally {
    await bobContext.close()
  }
})
