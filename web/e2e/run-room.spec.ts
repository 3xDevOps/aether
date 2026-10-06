// Two humans with separate gateways and browser contexts share one real run.

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
const comment = `${requestedAliceName} leaves ${requestedBobName} a note`
const deniedSteer = 'Please inspect the first failing test'
const approvedSteer = 'Please inspect the second failing test'

async function expectHitTarget(target: Locator) {
  await expect(target).toBeVisible()
  await expect.poll(() => target.evaluate((element) => {
    const box = element.getBoundingClientRect()
    return element.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2))
  })).toBe(true)
}

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('two members share notes, moderated messages, and explicit control transfer', async ({
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
  // Open Bob's browser before linking so he goes through real onboarding, not
  // just a gateway config change behind the dashboard.
  const bobContext = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const bobPage = await bobContext.newPage()
  try {
    const wizard = await OnboardingWizard.open(bobPage, bob.url)
    await wizard.expectStep('Connect')
    await wizard.connect.link(aether.server.addr, {
      invite,
      name: requestedBobName,
    })
    await wizard.connect.continue().click()
    await wizard.repository.use('project').click()
    await wizard.expectStep('Repository')
    await wizard.repository.localClone().click()
    const clone = await aether.cloneRepo(repo, 'project-bob')
    await wizard.repository.addRemote(clone)
    await expect(wizard.repository.section).toContainText(`Connected ${clone}`)
    await wizard.repository.continue().click()
    await wizard.expectStep('Agent')
    await wizard.agent.skip().click()
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
    await page.goto(`${alice.url}&run=${run.id}`)
    await bobPage.goto(`${bob.url}&run=${run.id}`)
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
    await expect(bobPage.getByRole('heading', { name: task, exact: true })).toBeVisible()
    const alicePresence = page.getByRole('group', { name: 'Run presence', exact: true })
    const bobPresence = bobPage.getByRole('group', { name: 'Run presence', exact: true })
    // The owner's desktop attach takes the lease; everyone else watches it.
    await expect(alicePresence.getByText('You control', { exact: true })).toBeVisible()
    await expect(alicePresence.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(bobPresence.getByText(`${aliceDisplayName} controls`, { exact: true })).toBeVisible()
    for (const viewerPage of [page, bobPage]) {
      const facts = viewerPage.getByRole('complementary', { name: 'Run details' }).getByRole('region', { name: 'Details' })
      await expect(facts.getByRole('img', { name: bobDisplayName, exact: true })).toBeVisible()
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
      // The owner answers a native question; everyone else sees it waiting on her.
      const aliceHeader = page.locator('[data-slot=pane-header]')
      const bobHeader = bobPage.locator('[data-slot=pane-header]')
      if (requests.length) {
        await expect(aliceHeader.getByText('Question: answer in the terminal', { exact: true })).toBeVisible()
        await expect(bobHeader.getByText(`Waiting for ${aliceDisplayName}`, { exact: true })).toBeVisible()
      } else {
        await expect(aliceHeader.getByText('Question: answer in the terminal', { exact: true })).toBeHidden()
      }
    }

    // The Details panel sits beside the terminal; hiding it gives the
    // terminal its width back, and the PTY follows the writer's new size.
    await expect.poll(() => terminalSizes.at(-1)?.cols ?? 0).toBeGreaterThan(0)
    const withDetails = terminalSizes.at(-1)!.cols
    for (const viewerPage of [page, bobPage]) {
      const details = viewerPage.getByRole('complementary', { name: 'Run details' })
      const terminal = viewerPage.getByRole('tabpanel').filter({ has: viewerPage.locator('.xterm') })
      const terminalBox = await terminal.boundingBox()
      const detailsBox = await details.boundingBox()
      if (!terminalBox || !detailsBox) throw new Error('the terminal and Run details must have visible geometry')
      expect(terminalBox.x + terminalBox.width).toBeLessThanOrEqual(detailsBox.x + 1)
      expect(Math.round(detailsBox.width)).toBe(320)
      for (const name of ['Hide details', 'More']) {
        await expectHitTarget(viewerPage.getByRole('button', { name, exact: true }))
      }
      const toolbar = viewerPage.getByRole('toolbar', { name: 'Terminal toolbar', exact: true })
      for (const button of await toolbar.getByRole('button').all()) {
        await expect(button).toBeInViewport({ ratio: 1 })
        if (await button.isEnabled()) await expectHitTarget(button)
      }
    }
    const terminalInput = page.locator('.xterm-helper-textarea:not([data-aether-frozen-view] *)')
    await terminalInput.focus()
    await page.keyboard.press('ControlOrMeta+Period')
    await expect(page.getByRole('complementary', { name: 'Run details' })).toBeHidden()
    await expect(terminalInput).toBeFocused()
    await expect.poll(() => terminalSizes.at(-1)?.cols ?? withDetails).toBeGreaterThan(withDetails)
    const withoutDetails = terminalSizes.at(-1)!.cols
    await page.keyboard.press('ControlOrMeta+Period')
    const aliceDetails = page.getByRole('complementary', { name: 'Run details' })
    await expect(aliceDetails).toBeVisible()
    await expect.poll(() => terminalSizes.at(-1)?.cols ?? withoutDetails).toBeLessThan(withoutDetails)
    const more = page.getByRole('button', { name: 'More', exact: true })
    await more.focus()
    await page.keyboard.press('Enter')
    await expect(page.getByRole('menu')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(more).toBeFocused()
    await testInfo.attach('desktop-run-details', { body: await page.screenshot(), contentType: 'image/png' })
    await testInfo.attach('desktop-run-observer', { body: await bobPage.screenshot(), contentType: 'image/png' })

    // A note is persisted once and the room event makes the other browser
    // refetch it; neither side relies on an optimistic echo.
    const bobDetails = bobPage.getByRole('complementary', { name: 'Run details' })
    await aliceDetails.getByRole('textbox', { name: 'Add a note' }).fill(comment)
    await aliceDetails.getByRole('button', { name: 'Add', exact: true }).click()
    await expect(aliceDetails.getByRole('region', { name: 'Notes' })).toContainText(comment)
    await expect(bobDetails.getByRole('region', { name: 'Notes' })).toContainText(comment)

    // Bob does not control the run: his message waits 45 seconds unless
    // Alice, who does, delivers or denies it first.
    await bobPage.getByRole('tab', { name: 'Session', exact: true }).click()
    const bobComposer = bobPage.getByRole('textbox', { name: 'Message the agent' })
    await expect(bobPage.getByText(/waits 45 s before it reaches the agent/)).toBeVisible()
    await bobComposer.fill(deniedSteer)
    await bobComposer.press('ControlOrMeta+Enter')
    await expect(bobComposer).toHaveValue('')
    const bobLog = bobPage.getByRole('log', { name: 'Session' })
    const deniedRow = bobLog.getByRole('article').filter({ hasText: deniedSteer })
    await expect(deniedRow).toContainText(/Delivers in (?:4[0-5])s/)
    const needsAlice = aliceDetails.getByRole('region', { name: 'Needs you' })
    const deniedCard = needsAlice.locator('[data-slot=request-card]').filter({ hasText: deniedSteer })
    await expect(deniedCard).toContainText(`${bobDisplayName} sent the agent a message`)
    await deniedCard.getByRole('button', { name: 'Deny', exact: true }).click()
    await expect(deniedCard).toBeHidden()
    await expect(deniedRow).toContainText('Denied')

    await bobComposer.fill(approvedSteer)
    await bobPage.getByRole('button', { name: 'Send', exact: true }).click()
    const approvedCard = needsAlice.locator('[data-slot=request-card]').filter({ hasText: approvedSteer })
    await approvedCard.getByRole('button', { name: 'Approve', exact: true }).click()
    await expect(bobLog.getByRole('article').filter({ hasText: approvedSteer })).toContainText('Sent')
    await bobPage.getByRole('tab', { name: 'Terminal', exact: true }).click()

    // A short occupied click reports the conflict without displacing Alice.
    const takeControl = bobPresence.getByRole('button', { name: 'Take control', exact: true })
    await takeControl.click()
    await expect(bobPage.getByText('run control is held by another session', { exact: true })).toBeVisible()
    await expect(bobPage.getByRole('alertdialog')).toBeHidden()
    await expect(bobPresence.getByRole('button', { name: 'Release', exact: true })).toBeHidden()
    await expect(alicePresence.getByRole('button', { name: 'Release', exact: true })).toBeVisible()

    // Hold through the server's five-second threshold; only the holder decides.
    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    const takeover = page.getByRole('alertdialog', { name: 'Terminal control requested' })
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await expect(takeover).toContainText(bobDisplayName)
    await takeover.getByRole('button', { name: 'Deny', exact: true }).click()
    await expect(takeover).toBeHidden()
    await expect(alicePresence.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(bobPresence.getByText(`${aliceDisplayName} controls`, { exact: true })).toBeVisible()
    await expect(takeControl).toHaveAttribute('aria-disabled', 'false')

    await takeControl.focus()
    await bobPage.keyboard.down('Space')
    await expect(takeover).toBeVisible()
    await bobPage.keyboard.up('Space')
    await takeover.getByRole('button', { name: 'Accept', exact: true }).click()

    await expect(bobPresence.getByRole('button', { name: 'Release', exact: true })).toBeVisible()
    await expect(alicePresence.getByText(`${bobDisplayName} controls`, { exact: true })).toBeVisible()
    // Alice remains an observer after the server fences her stale writable
    // attach; she must not retain a second input path.
    await expect(alicePresence.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()

    // Bob's lease ends only through an explicit release, and both browsers
    // observe the server's post-release controller state in place.
    await bobPresence.getByRole('button', { name: 'Release', exact: true }).click()
    await expect(bobPresence.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()
    await expect(bobPresence.getByText('Nobody controls', { exact: true })).toBeVisible()
    await expect(alicePresence.getByText('Nobody controls', { exact: true })).toBeVisible()

    // A teammate's question waits on the owner; her own would wait on others.
    await bob.api.rpc('run.room.post', {
      workspace_id: workspaces[0].id,
      run_id: run.id,
      kind: 'question',
      body: 'Which release branch should receive this change?',
      idempotency_key: 'run-room-e2e-unanswered-question',
    })

    // Wait for both server snapshots before reloading, so a failure separates
    // server-side propagation from SPA hydration timing.
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
    await expect(card.getByText('A teammate asked a question', { exact: true })).toBeVisible()
    await card.getByRole('button', { name: task, exact: true }).click()
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  } finally {
    await bobContext.close()
  }
})
