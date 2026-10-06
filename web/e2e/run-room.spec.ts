// A real two-human Run Room: separate gateways and browser contexts watch one
// long-running run, with comments, queued steering, moderation and control
// transfer all travelling through the server's durable room and attach paths.

import type { Locator } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { expect, test } from './fixtures'
import { runContainer } from './harness/docker'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

const requestedAliceName = 'Alice'
const requestedBobName = 'Bob'
const task = 'shared release A run room'
const comment = `${requestedAliceName} sees ${requestedBobName} in the room`
const deniedSteer = 'Please inspect the first failing test'
const approvedSteer = 'Please inspect the second failing test'

function messageRow(room: Locator, body: string): Locator {
  return room.locator('article').filter({ hasText: body })
}

async function expectHitTarget(target: Locator) {
  await expect(target).toBeVisible()
  await expect.poll(() => target.evaluate((element) => {
    const box = element.getBoundingClientRect()
    return element.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2))
  })).toBe(true)
}

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('two members share comments, moderated steering, and explicit control transfer', async ({
  page,
  browser,
  aether,
}, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member(requestedAliceName)
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const invite = await aether.invite(alice)
  const { member: aliceMember } = await alice.api.rpc<{
    member: { display_name: string }
  }>('server.info')
  const aliceDisplayName = aliceMember.display_name

  const bob = await aether.member(requestedBobName)
  // Keep Bob's independent browser ready before linking so this human goes
  // through the real onboarding state rather than only changing gateway
  // configuration behind the dashboard.
  const bobContext = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const bobPage = await bobContext.newPage()
  try {
    const wizard = await OnboardingWizard.open(bobPage, bob.url)
    await wizard.expectStep('Link')
    await wizard.link.link(aether.server.addr, {
      invite,
      name: requestedBobName,
    })
    await wizard.link.continue().click()
    await wizard.expectStep('Git identity')
    await wizard.gitIdentity.skip().click()
    await wizard.expectStep('Workspace')
    await wizard.workspace.use('project').click()
    await wizard.expectStep('Repository')
    await wizard.repository.localClone().click()
    const clone = await aether.cloneRepo(repo, 'project-bob')
    await wizard.repository.addRemote(clone)
    await expect(wizard.repository.section).toContainText(`Connected ${clone}`)
    await wizard.repository.continue().click()
    await wizard.expectStep('Agents')
    await wizard.agents.skip().click()
    const { member: bobMember } = await bob.api.rpc<{
      member: { display_name: string }
    }>('server.info')
    const bobDisplayName = bobMember.display_name

    // Keep the PTY alive while the two humans exercise the room and its lease.
    aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
    const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
      'workspace.list',
    )
    const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
      workspace_id: workspaces[0].id,
      harness: 'claude',
      task,
    })
    const terminalSizes: { cols: number; rows: number }[] = []
    page.on('websocket', (socket) => {
      if (!socket.url().includes(`/ws/attach/${run.id}`)) return
      socket.on('framesent', ({ payload }) => {
        if (typeof payload !== 'string') return
        const frame = JSON.parse(payload) as { cols?: number; rows?: number }
        if (typeof frame.cols === 'number' && typeof frame.rows === 'number') {
          terminalSizes.push({ cols: frame.cols, rows: frame.rows })
        }
      })
    })
    // The run id is the dashboard deep link used by both independent shells.
    await page.goto(`${alice.url}&run=${run.id}`)
    await bobPage.goto(`${bob.url}&run=${run.id}`)
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
    await expect(bobPage.getByRole('heading', { name: task, exact: true })).toBeVisible()
    // Both collapsed toolbars must discover the live attach watchers without
    // opening the room or loading its message history.
    for (const viewerPage of [page, bobPage]) {
      await expect(viewerPage.getByRole('complementary', { name: 'Run Room' })).toBeHidden()
      const viewers = viewerPage.getByRole('group', { name: 'Run viewers', exact: true })
      await expect(viewers.getByRole('img')).toHaveCount(2)
      await expect(viewers.getByRole('img', { name: aliceDisplayName, exact: true })).toBeVisible()
      await expect(viewers.getByRole('img', { name: bobDisplayName, exact: true })).toBeVisible()
    }

    const nativeRequest = { id: 'room-native-question', session_id: 'room-native-session', kind: 'question' }
    for (const requests of [[nativeRequest], []]) {
      execFileSync('docker', [
        'exec', runContainer(run.id), '/opt/aether/aether-server', 'report', 'pi', '--json',
        JSON.stringify({
          ...(requests.length ? { state: 'working' } : {}),
          input_updates: [{ operation: 'replace', requests }],
        }),
      ], { encoding: 'utf8', timeout: 15_000 })
      for (const viewerPage of [page, bobPage]) {
        const header = viewerPage.locator('header').filter({
          has: viewerPage.getByRole('heading', { name: task, exact: true }),
        })
        await expect(header.getByRole('button', { name: /^Requests:/ })).toHaveCount(requests.length)
        // The owner answers a native question; everyone else sees it waiting on her.
        const state = requests.length > 0 && viewerPage === page ? 'Needs you' : 'Working'
        await expect(header.getByText(state, { exact: true })).toBeVisible()
      }
    }

    await bobPage.getByRole('button', { name: 'Open Run Room' }).click()
    const bobRoom = bobPage.getByRole('complementary', { name: 'Run Room' })
    await expect(bobRoom).toBeVisible()
    const bobControls = bobPage.getByRole('group', { name: 'Terminal attachment controls', exact: true })
    const aliceControls = page.getByRole('group', { name: 'Terminal attachment controls', exact: true })
    await expect(bobControls.getByTitle(`Controller: ${aliceDisplayName}`, { exact: true })).toBeVisible()
    await expect(bobControls.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect.poll(() => terminalSizes.at(-1)?.cols ?? 0).toBeGreaterThan(0)
    const closedColumns = terminalSizes.at(-1)!.cols
    const terminalInput = page.locator('.xterm-helper-textarea:not([data-aether-frozen-view] *)')
    await terminalInput.focus()
    await page.keyboard.press('ControlOrMeta+Shift+M')
    const aliceRoom = page.getByRole('complementary', { name: 'Run Room' })
    const composer = aliceRoom.getByRole('textbox', { name: 'Run Room message' })
    await expect(composer).toBeFocused()
    await expect.poll(() => terminalSizes.at(-1)?.cols ?? closedColumns).toBeLessThan(closedColumns)
    const openColumns = terminalSizes.at(-1)!.cols

    for (const viewerPage of [page, bobPage]) {
      const room = viewerPage.getByRole('complementary', { name: 'Run Room' })
      const main = viewerPage.getByRole('tabpanel').filter({ has: viewerPage.locator('.xterm') })
      const mainBox = await main.boundingBox()
      const roomBox = await room.boundingBox()
      if (!mainBox || !roomBox) throw new Error('Run Room and terminal panel must have visible geometry')
      expect(mainBox.x + mainBox.width).toBeLessThanOrEqual(roomBox.x + 1)
      expect(roomBox.width).toBeLessThanOrEqual(420)
      expect(roomBox.y).toBeGreaterThan(0)
      await expect(room.getByText(/Controller:|Viewing:/)).toHaveCount(0)
      await expect(room.getByRole('button', { name: /Evidence|Take control|Release control/ })).toHaveCount(0)
      await expect(viewerPage.getByRole('button', { name: /Evidence/ })).toHaveCount(1)
      for (const name of ['Message', 'Pause', 'More']) {
        await expectHitTarget(viewerPage.getByRole('button', { name, exact: true }))
      }
      const attachment = viewerPage.getByRole('group', { name: 'Terminal attachment controls', exact: true })
      await expectHitTarget(attachment.getByRole('button', { name: /^(Take control|Release)$/ }))
      const tools = viewerPage.getByRole('button', { name: 'Terminal tools', exact: true })
      if (await tools.isVisible()) {
        await expectHitTarget(tools)
        await tools.click()
      }
      const toolbar = viewerPage.getByRole('toolbar', { name: 'Terminal controls', exact: true })
      await expect(toolbar).toBeVisible()
      for (const button of await toolbar.getByRole('button').all()) {
        await expect(button).toBeInViewport({ ratio: 1 })
        if (await button.isEnabled()) await expectHitTarget(button)
      }
      if (await tools.isVisible()) await viewerPage.keyboard.press('Escape')
    }

    await composer.fill('keep this keyboard draft')
    await page.keyboard.press('ControlOrMeta+Shift+M')
    await expect(aliceRoom).toBeHidden()
    await expect(terminalInput).toBeFocused()
    await expect.poll(() => terminalSizes.at(-1)?.cols ?? openColumns).toBeGreaterThan(openColumns)
    await page.keyboard.press('ControlOrMeta+Shift+M')
    await expect(composer).toBeFocused()
    await expect(composer).toHaveValue('keep this keyboard draft')
    await expect.poll(() => terminalSizes.at(-1)?.cols ?? closedColumns).toBeLessThan(closedColumns)
    const more = page.getByRole('button', { name: 'More', exact: true })
    await more.focus()
    await page.keyboard.press('Enter')
    await expect(page.getByRole('menu')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(more).toBeFocused()
    await testInfo.attach('desktop-run-room-docked', { body: await page.screenshot(), contentType: 'image/png' })
    await testInfo.attach('desktop-run-room-observer', { body: await bobPage.screenshot(), contentType: 'image/png' })

    // A comment is persisted once and the room event causes the other open
    // browser to refetch it; neither side relies on an optimistic echo.
    await aliceRoom.getByRole('textbox', { name: 'Run Room message' }).fill(comment)
    await aliceRoom.getByRole('button', { name: 'Send', exact: true }).click()
    const aliceComment = messageRow(aliceRoom, comment)
    await expect(aliceComment).toContainText('Posted')
    await expect(messageRow(bobRoom, comment)).toContainText('Posted')
    await expect(bobRoom).toContainText(comment)

    // Bob is a watcher, not the controller: Send to agent is allowed to
    // create a durable request, but its 45-second grace period prevents a
    // direct PTY write.
    await bobRoom.getByRole('button', { name: 'Send to agent' }).click()
    await bobRoom.getByRole('textbox', { name: 'Run Room message' }).fill(deniedSteer)
    await bobRoom.getByRole('button', { name: 'Queue steer', exact: true }).click()
    const deniedRow = messageRow(bobRoom, deniedSteer)
    await expect(deniedRow).toContainText('queued')
    await expect(deniedRow).toContainText(/(?:4[0-5])s before delivery/)
    const aliceDeniedRow = messageRow(aliceRoom, deniedSteer)
    await expect(aliceDeniedRow).toContainText(/before delivery/)
    await expect(aliceDeniedRow.getByRole('button', { name: 'Approve now' })).toBeVisible()
    await expect(aliceDeniedRow.getByRole('button', { name: 'Deny' })).toBeVisible()

    await aliceDeniedRow.getByRole('button', { name: 'Deny' }).click()
    await expect(aliceDeniedRow).toContainText('denied')
    await expect(messageRow(bobRoom, deniedSteer)).toContainText('denied')

    // A second request is approved by Alice while her live controller lease
    // still owns the PTY, producing a durable Sent receipt on both browsers.
    await bobRoom.getByRole('button', { name: 'Send to agent' }).click()
    await bobRoom.getByRole('textbox', { name: 'Run Room message' }).fill(approvedSteer)
    await bobRoom.getByRole('button', { name: 'Queue steer', exact: true }).click()
    const approvedRow = messageRow(aliceRoom, approvedSteer)
    await expect(approvedRow).toContainText(/before delivery/)
    await approvedRow.getByRole('button', { name: 'Approve now' }).click()
    await expect(approvedRow).toContainText('Sent')
    await expect(messageRow(bobRoom, approvedSteer)).toContainText('Sent')

    // A short occupied click reports the conflict without displacing Alice.
    const takeControl = bobControls.getByRole('button', { name: 'Take control', exact: true })
    await takeControl.click()
    await expect(bobControls.getByText('run control is held by another session', { exact: true })).toBeVisible()
    await expect(bobPage.getByRole('alertdialog')).toBeHidden()
    await expect(bobControls.getByRole('button', { name: 'Release', exact: true })).toBeHidden()
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(bobControls.getByTitle(`Controller: ${aliceDisplayName}`, { exact: true })).toBeVisible()

    // Hold through the server's five-second threshold; only the holder decides.
    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    const takeover = page.getByRole('alertdialog', { name: 'Terminal control requested' })
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await expect(takeover).toContainText(bobDisplayName)
    await takeover.getByRole('button', { name: 'Deny', exact: true }).click()
    await expect(takeover).toBeHidden()
    await expect(aliceControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(bobControls.getByTitle(`Controller: ${aliceDisplayName}`, { exact: true })).toBeVisible()
    await expect(takeControl).toHaveAttribute('aria-disabled', 'false')

    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await takeover.getByRole('button', { name: 'Accept', exact: true }).click()

    // The host controls follow the new lease while both docked rooms stay open.
    await expect(bobControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(aliceControls.getByTitle(`Controller: ${bobDisplayName}`, { exact: true })).toBeVisible()
    // Alice remains an observer after the server fences her stale writable
    // attach; she must not retain a second input path.
    await expect(aliceControls.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()

    // Bob's lease ends only through an explicit release, and both browsers
    // observe the server's post-release controller state in place.
    await bobControls.getByRole('button', { name: 'Release', exact: true }).click()
    await expect(bobControls.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()
    await expect(bobControls.getByText('Nobody', { exact: true })).toBeVisible()
    await expect(aliceControls.getByText('Nobody', { exact: true })).toBeVisible()

    await alice.api.rpc('run.room.post', {
      workspace_id: workspaces[0].id,
      run_id: run.id,
      kind: 'question',
      body: 'Which release branch should receive this change?',
      idempotency_key: 'run-room-e2e-unanswered-question',
    })

    // Wait for both authoritative run snapshot paths before reloading the
    // dashboard. This distinguishes server-side count propagation from SPA
    // hydration timing when the question event lands just after the post.
    await expect
      .poll(
        async () => {
          const [{ runs }, { run: snapshot }] = await Promise.all([
            alice.api.rpc<{ runs: { id: string; unanswered_questions?: number }[] }>('run.list'),
            alice.api.rpc<{ run: { id: string; unanswered_questions?: number } }>('run.get', { run_id: run.id }),
          ])
          return {
            list: runs.find((item) => item.id === run.id)?.unanswered_questions,
            get: snapshot.unanswered_questions,
          }
        },
        { timeout: 30_000, intervals: [100, 250, 500, 1_000] },
      )
      .toEqual({ list: 1, get: 1 })
    await page.goto(alice.url)
    const card = page.locator(`[data-run-id="${run.id}"]`)
    await expect(card.getByText(task, { exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: /Requests: 1 unanswered question/ })).toBeVisible()
    await expect(card.getByText('Open question in the Run Room', { exact: true })).toBeVisible()
    await card.getByRole('button', { name: task, exact: true }).click()
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  } finally {
    await bobContext.close()
  }
})
