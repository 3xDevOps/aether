// The Agent step's Agent config files section through a real browser,
// gateway and server: the prompt copies byte for byte, and Open remote files
// lands on the Files tree with Home unfolded, where the agent's uploads show.

import { configPrompt } from '../src/lib/config-prompt'
import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

test('the Agent step copies the config prompt and opens the uploads in Files', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  await wizard.connect.continue().click()
  await wizard.repository.createFromClone('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agent')

  // Optional, so it waits behind its own disclosure.
  await expect(wizard.agent.section.getByRole('region', { name: 'Agent config files' })).toHaveCount(0)
  const configuration = await wizard.agent.configuration()
  await expect(configuration.prompt()).toBeVisible()
  expect(await configuration.prompt().textContent()).toBe(configPrompt)

  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], {
    origin: new URL(alice.url).origin,
  })
  await configuration.copy().click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(configPrompt)

  // Stands in for the agent's `aether config push`.
  await alice.api.rpc('config.write', {
    harness: 'claude',
    path: 'settings.json',
    content: '{"theme":"dark"}\n',
    revision: '',
  })
  await configuration.openRemoteFiles().click()
  await expect(page.getByRole('heading', { name: 'Files', exact: true })).toBeAttached()
  const tree = page.getByRole('complementary', { name: 'Files' })
  await expect(tree.getByRole('button', { name: /^Home\s?~$/ })).toHaveAttribute('aria-expanded', 'true')
  await tree.getByRole('button', { name: '.claude', exact: true }).click()
  await expect(tree.getByRole('button', { name: 'settings.json', exact: true })).toBeVisible()
})
