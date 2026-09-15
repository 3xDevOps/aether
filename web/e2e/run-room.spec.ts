// A real two-human Run Room: separate gateways and browser contexts watch one
// long-running run, with comments, queued steering, moderation and control
// transfer all travelling through the server's durable room and attach paths.

import type { Locator } from '@playwright/test'
import { expect, test } from './fixtures'
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

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('two members share comments, moderated steering, and explicit control transfer', async ({
  page,
  browser,
  aether,
}) => {
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
    // The run id is the dashboard deep link used by both independent shells.
    await page.goto(`${alice.url}&run=${run.id}`)
    await bobPage.goto(`${bob.url}&run=${run.id}`)
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
    await expect(bobPage.getByRole('heading', { name: task, exact: true })).toBeVisible()
    await expect(page.getByText('Attached', { exact: true })).toBeVisible()
    await expect(bobPage.getByText('Attached', { exact: true })).toBeVisible()

    // Load Bob first so Alice's room status sees both attach watchers.
    await bobPage.getByRole('button', { name: 'Open Run Room' }).click()
    const bobRoom = bobPage.getByRole('complementary', { name: 'Run Room' })
    await expect(bobRoom).toBeVisible()
    await expect(bobRoom).toContainText('2 watching')
    await expect(bobRoom).toContainText(`Controller: ${aliceDisplayName}`)
    await expect(
      bobRoom.getByRole('img', { name: aliceDisplayName, exact: true }),
    ).toBeVisible()
    await expect(
      bobRoom.getByRole('img', { name: bobDisplayName, exact: true }),
    ).toBeVisible()
    await expect(bobRoom.getByRole('button', { name: 'Take control' })).toBeVisible()

    await page.getByRole('button', { name: 'Open Run Room' }).click()
    const aliceRoom = page.getByRole('complementary', { name: 'Run Room' })
    await expect(aliceRoom).toBeVisible()
    await expect(aliceRoom).toContainText('2 watching')
    await expect(aliceRoom).toContainText(`Controller: ${aliceDisplayName}`)
    await expect(
      aliceRoom.getByRole('img', { name: aliceDisplayName, exact: true }),
    ).toBeVisible()
    await expect(
      aliceRoom.getByRole('img', { name: bobDisplayName, exact: true }),
    ).toBeVisible()
    await expect(page.getByRole('button', { name: 'Release control' })).toBeVisible()

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

    // Occupied control cannot transfer implicitly: Bob must see Alice named
    // in a confirmation dialog before the takeover attach is sent.
    await bobRoom.getByRole('button', { name: 'Take control' }).click()
    const takeover = bobPage.getByRole('dialog')
    await expect(takeover).toBeVisible()
    await expect(takeover.getByRole('heading', { name: 'Take control of this run?', exact: true })).toBeVisible()
    await expect(takeover).toContainText(
      `${aliceDisplayName} currently controls the run`,
    )
    await takeover.getByRole('button', { name: 'Take control', exact: true }).click()

    await expect(bobPage.getByRole('button', { name: 'Steering', exact: true })).toBeVisible()
    // Control status is fetched when the room opens. Reopen it after the
    // takeover so the displayed controller comes from the server's new lease,
    // not the pre-takeover snapshot.
    await bobPage.getByRole('button', { name: 'Close Run Room' }).click()
    await bobPage.getByRole('button', { name: 'Open Run Room' }).click()
    const bobControlledRoom = bobPage.getByRole('complementary', { name: 'Run Room' })
    await expect(bobControlledRoom).toContainText(
      `Controller: ${bobDisplayName}`,
    )
    await expect(bobControlledRoom.getByRole('button', { name: 'Release control' })).toBeVisible()
    // Alice remains an observer after the server fences her stale writable
    // attach; she must not retain a second input path.
    await expect(aliceRoom.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()

    // Bob's lease ends only through an explicit release. Reopening the room
    // asks the server for the post-release status rather than trusting a stale
    // controller label in the previous snapshot.
    await bobControlledRoom.getByRole('button', { name: 'Release control' }).click()
    await expect(bobControlledRoom.getByRole('button', { name: 'Take control', exact: true })).toBeVisible()
    await bobPage.getByRole('button', { name: 'Close Run Room' }).click()
    await bobPage.getByRole('button', { name: 'Open Run Room' }).click()
    const releasedRoom = bobPage.getByRole('complementary', { name: 'Run Room' })
    await expect(releasedRoom).toContainText('No controller')

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
    const needsYou = page.getByRole('region', { name: 'Needs you' })
    await expect(needsYou.getByText(task, { exact: true })).toBeVisible()
    await expect(
      needsYou.getByText('1 unanswered question - open Run Room to answer', {
        exact: true,
      }),
    ).toBeVisible()
    await needsYou.getByRole('button', { name: task, exact: true }).click()
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  } finally {
    await bobContext.close()
  }
})
