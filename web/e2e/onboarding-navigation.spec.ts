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
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  await wizard.connect.continue().click()

  await wizard.expectStep('Repository')
  await wizard.back().click()
  await wizard.expectStep('Connect')
  await expect(wizard.connect.section).toContainText('Linked to')

  await wizard.connect.continue().click()
  await wizard.repository.createFromClone('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agent')
  await wizard.back().click()
  await wizard.expectStep('Repository')
  await expect(wizard.repository.section).toContainText(`Connected ${repo}`)

  // The workspace list is one click away, and picking the same workspace
  // again keeps its clone.
  await wizard.repository.change().click()
  await wizard.repository.use('project').click()
  await expect(wizard.repository.section).toContainText(`Connected ${repo}`)

  await wizard.repository.continue().click()
  await wizard.agent.skip().click()
  await wizard.expectStep('First run')
  await wizard.back().click()
  await wizard.expectStep('Agent')

  // A step already reached is one click away in the header.
  await page.getByRole('list', { name: 'Steps' }).getByRole('button', { name: /Repository, visited/ }).click()
  await wizard.expectStep('Repository')
})
