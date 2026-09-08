// The last step: a real run, in a real container, launched from the wizard.
// The fake harness runs the `agent.sh` the seed repository carries, exits
// cleanly, and the run parks at needs-attention with its work committed.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { OnboardingWizard } from './pages/wizard'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('the first run reaches needs-attention', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  // The git identity is optional and this scenario is not about it.
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  // Every run forks from the workspace's base branch, so the push is what
  // makes a first run possible at all.
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await wizard.repository.continue().click()
  await wizard.agents.skip().click()

  await wizard.expectStep('First run')
  await wizard.firstRun.launch('fake', 'write the result file')

  // Launching leaves the wizard for the run it created, which is where the
  // run's own state arrives over the event stream.
  await expect(
    page.getByRole('heading', { name: 'write the result file', exact: true }),
  ).toBeVisible()
  const state = page.getByRole('definition').first()
  await expect(state).toContainText('Needs you', { timeout: 3 * 60 * 1000 })
  await expect(state).toContainText('agent exited; results committed')
})
