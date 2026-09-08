// The Agents step's first half: the setup screen, the environment container
// it opens a terminal into, and the confirmation that saves the environment
// so an install reaches the image runs start from.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

test.skip(!dockerReachable(), 'the environment terminal needs a reachable Docker daemon')

test('setting an agent up saves the environment', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')

  await wizard.agents.setUp('Claude Code').click()
  await expect(wizard.agents.section).toContainText('Set up claude')
  await expect(wizard.agents.section).toContainText('curl -fsSL https://claude.ai/install.sh | bash')
  // The environment terminal starts a real container on first open.
  await expect(wizard.agents.containerStarting()).toHaveText(
    'Starting your environment container',
  )
  await expect(wizard.agents.containerStarting()).toHaveCount(0, {
    timeout: 3 * 60 * 1000,
  })

  // Back closes the sub-screen before it leaves the step.
  await wizard.back().click()
  await wizard.expectStep('Agents')
  await expect(wizard.agents.setUp('Claude Code')).toBeVisible()

  // The install a person does in that terminal, done to the same directory:
  // the member's environment home, which agent.list reads.
  aether.installAgent(await memberID(alice), 'claude')

  await wizard.agents.setUp('Claude Code').click()
  await wizard.agents.confirmInstalled().click()
  await expect(wizard.agents.section).toContainText('Agent installed')
  await expect(wizard.agents.section).toContainText('your environment is saved as')
  await expect(wizard.agents.section).toContainText('aether/member-')
})
