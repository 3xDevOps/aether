// An Enhanced run's Session view against a real server: the acpmock agent
// runs as the run's ACP adapter, the dashboard streams /ws/acp, answers a
// permission natively, sends a message, queues one behind a turn and interrupts it.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { runCoordCLI } from './harness/coord'
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
  await box.press('Enter')
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
  await box.press('Enter')
  await expect(card.getByText('rm -rf build')).toBeVisible()
  await expect(card).not.toBeFocused()
  await card.getByRole('button', { name: 'Allow', exact: true }).click()
  await expect(log.getByText('permission: allow', { exact: true })).toHaveCount(2)
  await page.screenshot({ path: testInfo.outputPath('enhanced-first-click-approval.png') })

  await box.fill('demo')
  await box.press('Enter')
  await expect(log.getByRole('button', { name: /Read 2 files, ran 1 command and edited 1 file/ })).toBeVisible({ timeout: 30_000 })
  await expect(log.getByText('Changed 1 file')).toBeVisible()
  await log.getByRole('button', { name: /Read 2 files, ran 1 command and edited 1 file/ }).click()
  await log.getByRole('button', { name: /Edited src\/billing\.js/ }).click()
  await expect(log.locator('[data-slot="diff-block"]')).toContainText('Math.round')
  // The mock advertises steering but does not implement it, so wait for the turn to end.
  await expect(log.getByText(/^Finished in \d+s$/)).toBeVisible()

  await box.fill('wait')
  await box.press('Enter')
  const interrupt = page.getByRole('button', { name: 'Interrupt the agent' })
  await expect(interrupt).toBeVisible()
  await box.fill('after the wait')
  await box.press('ControlOrMeta+Shift+Enter')
  const queued = log.locator('[data-slot="message-row"]', { hasText: 'after the wait' })
  await expect(queued).toContainText('Queued')
  await interrupt.click()
  await expect(log.getByText(/^Cancelled/)).toBeVisible()
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

test('an Enhanced run holding background work stays working, asks while it waits and stops on Stop waiting', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installClaudeACPMock(await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspace = workspaces[0].id
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspace, harness: 'claude', mode: 'acp', task: 'demo' })
  const status = async () => (await alice.api.rpc<{ run: { status: string; reason?: string } }>('run.get', { run_id: run.id })).run
  const statuses = async () => (await alice.api.rpc<{ events: { seq: number; payload: { to: string; reason?: string } }[] }>(
    'workspace.timeline', { workspace_id: workspace, run_id: run.id, types: ['run.status'], newest: true, limit: 1000 })).events

  await page.goto(`${alice.url}&run=${run.id}`)
  const log = page.getByRole('log', { name: 'Session' })
  const runs = page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' })
  const header = page.locator('[data-slot="pane-header"]')
  const box = page.getByRole('combobox', { name: 'Message the agent' })
  await expect(log.getByText(/^Finished/)).toBeVisible({ timeout: 60_000 })

  await box.fill('hold in background')
  await box.press('Enter')
  await expect(log.getByText('started', { exact: true })).toBeVisible()
  await expect(runs.getByRole('button', { name: /^Working · .* · Working in background: 1 task/ })).toBeVisible()
  await expect(header.getByText('Working in background: 1 task')).toBeVisible()
  // Past acpmock's 2 s BackgroundDelay, the held task still keeps the run working.
  await page.waitForTimeout(3_000)
  expect(await status()).toMatchObject({ status: 'running', reason: 'working in background: 1 task' })
  await expect(runs.getByRole('button', { name: /^Needs you/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Stop waiting for background work' })).toBeVisible()

  runCoordCLI(run.id, ['report', '--outcome', 'needs-input', '--summary', 'Which port should the server use?', '--idempotency-key', 'held-question'])
  await expect(header.getByText('Agent asks: Which port should the server use?')).toBeVisible({ timeout: 30_000 })
  await expect(runs.getByRole('button', { name: /^Needs you · .* · Agent asks: Which port should the server use\?/ })).toBeVisible()

  const released = Math.max(0, ...(await statuses()).map((event) => event.seq))
  await box.fill('release background')
  await box.press('Enter')
  await expect(log.getByText('background done: mock-held-task')).toBeVisible({ timeout: 30_000 })
  await expect.poll(() => status(), { timeout: 30_000 }).toMatchObject({ status: 'needs-attention', reason: 'agent idle' })
  // The release turn, the task's end and its follow-up turn settle once.
  await page.waitForTimeout(3_000)
  const parked = (await statuses()).filter((event) => event.seq > released && event.payload.to === 'needs-attention')
  expect(parked.map((event) => event.payload.reason)).toEqual(['agent idle'])
  await expect(header.getByText('Waiting for your reply')).toBeVisible()

  await box.fill('hold in background')
  await box.press('Enter')
  await expect(header.getByText('Working in background: 1 task')).toBeVisible({ timeout: 30_000 })
  await page.getByRole('button', { name: 'Stop waiting for background work' }).click()
  await expect(header.getByText('Turn cancelled')).toBeVisible()
  await expect.poll(() => status()).toMatchObject({ status: 'needs-attention', reason: 'turn cancelled' })
  await expect(page.getByRole('button', { name: 'Stop waiting for background work' })).toHaveCount(0)
})

test('an Enhanced run keeps subagents out of the conversation and opens each in the inspector', async ({ page, aether }, testInfo) => {
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

  const changed = log.locator('[data-slot="changed-files"]')
  const changedBefore = await changed.count()
  await box.fill('subagents')
  await box.press('Enter')
  await expect(log.getByText('All three subagents finished.')).toBeVisible({ timeout: 30_000 })
  await expect(log.getByText('Splitting the work across subagents.')).toBeVisible()
  await expect(log.getByRole('button', { name: '3 subagents' })).toHaveAttribute('aria-expanded', 'true')
  const billing = log.getByRole('button', { name: 'Map the billing module, Explore, Done' })
  await expect(billing).toBeVisible()
  await expect(log.getByRole('button', { name: 'Run the tests, general-purpose, Done' })).toBeVisible()
  await expect(log.getByRole('button', { name: 'Round totals to cents, Done' })).toBeVisible()
  await expect(log.getByText('Reading the billing module.')).toHaveCount(0)
  await expect(log.getByText('Read src/billing.js')).toHaveCount(0)
  await expect(changed).toHaveCount(changedBefore + 1)
  await expect(changed.last()).toContainText('src/billing.js')

  await billing.click()
  await expect(page).toHaveURL(/&agent=/)
  const inspector = page.getByRole('complementary', { name: 'Subagent' })
  const transcript = inspector.getByRole('log', { name: 'Subagent transcript' })
  const path = inspector.getByRole('navigation', { name: 'Subagent path' })
  await expect(path).toContainText('Session')
  await expect(path).toContainText('Map the billing module')
  await expect(transcript.getByText('Reading the billing module.')).toBeVisible()
  await expect(transcript.getByRole('button', { name: /Read 1 file/ })).toBeVisible()
  await expect(inspector.getByText('1 of 3')).toBeVisible()
  await expect(inspector.getByRole('button', { name: 'Previous subagent' })).toBeVisible()
  await expect(inspector.getByRole('button', { name: 'Close subagent' })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('enhanced-subagents.png') })

  await transcript.getByRole('button', { name: 'Find the callers, general-purpose, Done' }).click()
  await expect(path).toContainText('Map the billing module')
  await expect(path).toContainText('Find the callers')
  await expect(transcript.getByText('Two callers found.')).toBeVisible()
  await path.getByRole('button', { name: 'Map the billing module' }).click()
  await expect(transcript.getByText('Reading the billing module.')).toBeVisible()

  await inspector.getByRole('button', { name: 'Next subagent' }).click()
  await expect(transcript.getByText('Running the tests.')).toBeVisible()
  await expect(inspector.getByText('2 of 3')).toBeVisible()

  await page.reload()
  await expect(page).toHaveURL(/&agent=/)
  await expect(inspector.getByText('2 of 3')).toBeVisible({ timeout: 30_000 })
  await expect(transcript.getByText('Running the tests.')).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(inspector).toBeHidden()
  await expect(page).not.toHaveURL(/&agent=/)
  await page.goBack()
  await expect(transcript.getByText('Two callers found.')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(inspector).toBeHidden()

  await box.fill('workflow')
  await box.press('Enter')
  await expect(log.getByText('The workflow is running.')).toBeVisible({ timeout: 30_000 })
  const workflow = log.getByRole('button', { name: 'audit-billing, Workflow, Running' })
  await expect(workflow).toBeVisible()
  await expect(workflow).toContainText('Running in the background')
  const background = log.getByRole('button', { name: 'Watch the build, general-purpose, Running' })
  await expect(background).toContainText('Reading build.log')
  await expect(log.getByText('Async agent launched successfully.')).toHaveCount(0)
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
    const aliceBorder = page.locator('svg[data-control-appearance]')
    await expect(aliceBorder).toHaveAttribute('data-control-appearance', 'active')
    await bobPage.goto(`${bob.url}&run=${run.id}`)
    const bobControls = bobPage.getByRole('group', { name: 'Multiplayer controls' })
    const takeControl = bobControls.getByRole('button', { name: 'Take control', exact: true })
    await expect(bobControls).toContainText(`${aliceMember.display_name} controls`)
    await expect(takeControl).toHaveAttribute('aria-disabled', 'false')
    const bobBorder = bobPage.locator('svg[data-control-appearance]')
    await expect(bobBorder).toHaveAttribute('data-control-appearance', 'hidden')
    await expect(aliceControls.getByRole('group', { name: 'Run controller' }).getByRole('img', { name: aliceMember.display_name })).toBeVisible()
    await expect(aliceControls.getByRole('group', { name: 'Run controller' })).toContainText(aliceMember.display_name)
    await expect(bobControls.getByRole('group', { name: 'Run controller' }).getByRole('img', { name: aliceMember.display_name })).toBeVisible()
    const watchers = bobControls.getByRole('group', { name: 'Run watchers' })
    for (const name of [aliceMember.display_name, bobMember.display_name]) {
      await expect(watchers.getByText(name, { exact: true })).toBeVisible()
      await expect(watchers.getByRole('img', { name, exact: true })).toBeVisible()
    }

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
    await ownerComposer.press('Enter')
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
    await expect(aliceBorder.locator('[data-takeover-border]')).toBeAttached()
    await bobPage.keyboard.up('Space')
    await expect(takeControl).toHaveText('Take control')
    await expect(takeover).toBeHidden()
    await expect(aliceBorder.locator('[data-takeover-border]')).toHaveCount(0)
    await expect(aliceBorder).toHaveAttribute('data-control-appearance', 'active')
    await page.emulateMedia({ reducedMotion: 'reduce' })

    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    await expect(aliceBorder.locator('[data-takeover-border]')).toHaveCSS('stroke-dashoffset', '0px')
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await expect(takeover).toContainText(bobMember.display_name)
    await takeover.getByRole('button', { name: 'Deny', exact: true }).click()
    await expect(takeover).toBeHidden()
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(takeControl).toHaveAttribute('aria-disabled', 'false')
    await expect(aliceBorder.locator('[data-takeover-border]')).toHaveCount(0)
    await expect(aliceBorder).toHaveAttribute('data-control-appearance', 'active')
    await page.emulateMedia({ reducedMotion: 'no-preference' })

    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await expect(takeover.getByRole('status')).toContainText('Control transfers automatically')
    await page.screenshot({ path: testInfo.outputPath('enhanced-takeover-review.png') })
    await expect(bobControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible({ timeout: 15_000 })
    await expect(bobControls.getByRole('group', { name: 'Run controller' }).getByRole('img', { name: bobMember.display_name })).toBeVisible()
    await expect(bobControls.getByRole('group', { name: 'Run controller' })).toContainText(bobMember.display_name)
    await expect(aliceControls).toContainText(`${bobMember.display_name} controls`)
    await expect(aliceControls.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()
    await expect(aliceBorder).toHaveAttribute('data-control-appearance', 'takeover')
    await expect(bobBorder).toHaveAttribute('data-control-appearance', 'active')
    await bobControls.getByRole('button', { name: 'Release', exact: true }).click()
    await expect(bobControls).toContainText('Nobody controls')
    await expect(aliceControls).toContainText('Nobody controls')
    await expect(bobBorder).toHaveAttribute('data-control-appearance', 'release')

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
    await expect(watchers.getByText(bobMember.display_name, { exact: true })).toBeVisible()
    await expect(watchers.getByRole('img', { name: bobMember.display_name, exact: true })).toBeVisible()
    const bounds = await bobControls.boundingBox()
    expect(bounds).not.toBeNull()
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390)
    await bobPage.screenshot({ path: testInfo.outputPath('enhanced-multiplayer-phone.png') })
  } finally {
    await bobContext.close()
  }
})
