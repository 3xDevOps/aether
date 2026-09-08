// The second member joining a workspace someone else already seeded. The
// step must not offer a push git would reject: it compares the clone with
// the workspace and says the workspace already has the branch.

import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

test('a second member finds the workspace already seeded', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  const commit = await seedWorkspace(alice, aether.server.addr, repo)
  const code = await aether.invite(alice)

  const bob = await aether.member('bob')
  const clone = await aether.cloneRepo(repo, 'project-clone')
  const wizard = await OnboardingWizard.open(page, bob.url)

  await wizard.link.link(aether.server.addr, { invite: code, name: 'Bob' })
  await expect(wizard.link.section).toContainText('(collaborator)')
  await wizard.link.continue().click()
  // The git identity is optional and this scenario is not about it.
  await wizard.gitIdentity.skip().click()

  // The workspace is already there, so this step picks rather than creates.
  await wizard.expectStep('Workspace')
  await wizard.workspace.use('project').click()

  await wizard.expectStep('Repository')
  await wizard.repository.addRemote(clone)
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText(
    `Workspace already has main at ${commit.slice(0, 7)}. Nothing to push.`,
  )
  // Nothing was pushed, so the copyable `git push` is gone with the offer.
  await expect(wizard.repository.push()).toHaveCount(0)
  await expect(wizard.repository.gitOutput()).toBeVisible()
})
