// A browser directory import is explicit and one-time. The fixture directory
// includes an empty file and a scanner finding; the empty file is carried and
// the finding is reported by the server without blocking the rest of import.

import { fileURLToPath } from 'node:url'

import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

const claudeFixture = fileURLToPath(new URL('./testdata/claude-profile', import.meta.url))

test('a directory import reports server exclusions and writes the remote config', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
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
  await configuration.chooseDirectory(claudeFixture)
  await expect(configuration.preview()).toBeVisible()
  await expect(configuration.section).toContainText('claude-profile')

  // The fixture basename is intentionally not a harness root. The small
  // destination selector is the explicit fallback for such directories.
  await configuration.destination().selectOption('claude')
  await configuration.import().click()

  await expect(configuration.section).toContainText('Imported 5 files')
  await expect(configuration.section).toContainText('README.md')
  await expect(configuration.section).toContainText('secret')
  await expect(configuration.import()).toHaveCount(0)
  const settings = await alice.api.rpc<{ content: string; revision: string }>('config.read', {
    harness: 'claude',
    path: 'settings.json',
  })
  expect(settings.content).toContain('"theme": "dark"')
  const empty = await alice.api.rpc<{ content: string; size: number }>('config.read', {
    harness: 'claude',
    path: 'skills/deploy/notes.md',
  })
  expect(empty.content).toBe('')
  expect(empty.size).toBe(0)
  const edited = await alice.api.rpc<{ content: string }>('config.write', {
    harness: 'claude',
    path: 'settings.json',
    content: '{"theme":"light"}',
    revision: settings.revision,
  })
  expect(edited.content).toBe('{"theme":"light"}')
})
