// The first member on a fresh server, the whole way: link, create the
// workspace, point a clone at it, push, and read what git said.

import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

test('the first member links, creates a workspace and seeds it', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  aether.giveGitIdentity(alice, 'Alice Local', 'alice@local.invalid')
  const wizard = await OnboardingWizard.open(page, alice.url)

  await wizard.expectStep('Link')
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  // The first identity to authenticate on a fresh server becomes the admin,
  // and the gateway had no SSH key until this step made one.
  await expect(wizard.link.section).toContainText('Linked to')
  await expect(wizard.link.section).toContainText('(admin)')
  await expect(wizard.link.section).toContainText(
    `Created SSH key ${alice.home}/.ssh/id_ed25519`,
  )
  await wizard.link.continue().click()

  // What every commit made in this member's runs is authored as. The step
  // offers this machine's own git config, and saving moves the wizard on.
  await wizard.expectStep('Git identity')
  await expect(
    wizard.gitIdentity.section.getByLabel('Name', { exact: true }),
  ).toHaveValue('Alice Local')
  await expect(
    wizard.gitIdentity.section.getByLabel('Email', { exact: true }),
  ).toHaveValue('alice@local.invalid')
  await wizard.gitIdentity.save('Alice Lovelace', 'alice@example.com')

  await wizard.expectStep('Workspace')
  await wizard.workspace.create('project')

  await wizard.expectStep('Repository')
  await wizard.repository.addRemote(repo)
  await expect(wizard.repository.section).toContainText(`Connected ${repo}`)
  await expect(wizard.repository.section).toContainText('ssh://aether@')

  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  // git's own answer, not a summary of it.
  await expect(wizard.repository.gitOutput()).toContainText('[new branch]      main -> main')

  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')
})
