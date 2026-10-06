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

  await wizard.expectStep('Connect')
  await expect(wizard.page.getByRole('list', { name: 'Steps' })).toHaveText(/Connect.*Repository.*Agent.*First run/)
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  // The first identity to authenticate on a fresh server becomes the admin,
  // and the gateway had no SSH key until this step made one.
  await expect(wizard.connect.section).toContainText('Linked to')
  await expect(wizard.connect.section).toContainText('(admin)')
  await expect(wizard.connect.section).toContainText(
    `Created SSH key ${alice.home}/.ssh/id_ed25519`,
  )

  // What every commit made in this member's runs is authored as. Connect
  // offers this machine's own git config once the server is linked.
  await expect(wizard.connect.identity.form.getByLabel('Name', { exact: true })).toHaveValue('Alice Local')
  await expect(wizard.connect.identity.form.getByLabel('Email', { exact: true })).toHaveValue('alice@local.invalid')
  await wizard.connect.identity.save('Alice Lovelace', 'alice@example.com')
  await wizard.connect.continue().click()

  await wizard.expectStep('Repository')
  await expect(wizard.repository.section).toContainText('A workspace is one repository and base branch')
  await wizard.repository.createFromClone('project')
  await wizard.repository.addRemote(repo)
  await expect(wizard.repository.section).toContainText(`Connected ${repo}`)
  await expect(wizard.repository.section).toContainText('ssh://aether@')

  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  // git's own answer, not a summary of it.
  await expect(wizard.repository.gitOutput()).toContainText('[new branch]      main -> main')

  await wizard.repository.continue().click()
  await wizard.expectStep('Agent')
})
