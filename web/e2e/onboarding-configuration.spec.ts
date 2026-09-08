// Bringing a member's own agent configuration across. The fixture profile
// holds an empty file and a file whose content trips the secret scanner:
// the flagged file is named on the row and left out, the empty one is
// carried, and the push succeeds either way.

import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

test('a flagged file is left out and the rest of the profile imports', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  aether.giveClaudeProfile(alice)
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  // The git identity is optional and this scenario is not about it.
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')

  const configuration = wizard.agents.configuration
  await configuration.look().click()

  const row = configuration.row('Claude Code')
  await expect(row).toContainText(`${alice.home}/.claude`)
  await expect(row).toContainText(
    '1 file you wrote tripped the secret scanner. It is left out and the rest of this profile still imports.',
  )
  await expect(row).toContainText('skills/deploy/README.md')
  await expect(row).toContainText('secret detected (curl-auth-header)')

  await configuration.select('Claude Code').check()
  await configuration.import().click()

  await expect(row).toContainText('Imported 5 files')
  await expect(row).toContainText('skills/deploy/README.md was not sent')
})
