// Back, from every screen the wizard has. Going back must never lose what a
// step already settled: the workspace that was created, the clone that was
// connected.

import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

test('back walks the steps without losing what they settled', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()

  await wizard.expectStep('Workspace')
  await wizard.back().click()
  await wizard.expectStep('Link')

  await wizard.link.continue().click()
  await wizard.workspace.create('project')
  await wizard.expectStep('Repository')
  await wizard.back().click()
  // The workspace exists now, so this step lists it rather than offering
  // the creation form again.
  await wizard.expectStep('Workspace')
  await expect(wizard.workspace.use('project')).toBeVisible()

  await wizard.workspace.use('project').click()
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')
  await wizard.back().click()
  await wizard.expectStep('Repository')
  await expect(wizard.repository.section).toContainText(`Connected ${repo}`)

  await wizard.repository.continue().click()
  await wizard.agents.skip().click()
  await wizard.expectStep('First run')
  await wizard.back().click()
  await wizard.expectStep('Agents')
})
