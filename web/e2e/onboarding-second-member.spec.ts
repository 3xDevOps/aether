import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

test('a collaborator links a seeded workspace without an unconfirmed base push', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
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
  await wizard.repository.localClone().click()
  await wizard.repository.addRemote(clone)
  await expect(wizard.repository.continue()).toBeEnabled()
  await expect(wizard.repository.push()).toHaveCount(0)
  await expect(wizard.repository.section.getByLabel('Push command', { exact: true })).toHaveCount(0)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')
})
