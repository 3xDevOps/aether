// The Agent step's setup: the Standard and Enhanced comparison, the
// server-side install through agent.install, the environment terminal the
// login happens in, and the check that reads agent.list back. A stub `npm`
// in the member's environment home stands in for the registry; the install
// command, the container it runs in and everything agent.list reports are
// real.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

test.skip(!dockerReachable(), 'agent.install and the environment terminal need a reachable Docker daemon')

async function toAgentStep(page: import('@playwright/test').Page, aether: import('./fixtures').Aether) {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  await wizard.connect.continue().click()
  await wizard.repository.createFromClone('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agent')
  const id = await memberID(alice)
  return { wizard, id, home: aether.server.memberHome(id) }
}

test('setting an agent up installs it, opens its login, and checks it', async ({ page, aether }) => {
  const { wizard, id, home } = await toAgentStep(page, aether)
  const agent = wizard.agent
  aether.installStubNpm(id)

  await expect(agent.row('Codex')).toContainText('Not installed')
  await agent.setUp('Codex').click()
  // Codex starts in Standard until its adapter is installed: the default
  // selection is agent.list's default_mode.
  await expect(agent.mode('Standard')).toHaveAttribute('aria-checked', 'true')
  await expect(agent.section).toContainText('Codex: supported through an adapter')
  await expect(agent.section).toContainText('Chosen when the run starts')

  await agent.install('Codex').click()
  // The terminal opens for the login once the agent is there, with the
  // vendor's login command typed. The container starts on first open.
  await expect(agent.section).toContainText('codex login', { timeout: 5 * 60 * 1000 })
  await expect(agent.containerStarting()).toHaveCount(0, { timeout: 3 * 60 * 1000 })
  const status = agent.status('Codex')
  await expect(status).toContainText('Installed')
  await expect(status).toContainText('No login found')
  await expect(agent.section).not.toContainText(/signed in/i)
  await expect(agent.done()).toHaveCount(0)
  expect(readFileSync(path.join(home, 'npm-calls.log'), 'utf8')).toBe('install -g --prefix /root/.local @openai/codex\n')

  // The login a person finishes in that terminal, written where agent.list
  // looks for it.
  aether.giveCodexLogin(id)
  await agent.check().click()
  await expect(status).toContainText('Login found')
  await agent.done().click()

  await expect(agent.row('Codex')).toContainText('Installed · Login found')
  await expect(agent.section.getByRole('button', { name: 'Run Codex', exact: true })).toBeVisible()
  await agent.continue().click()
  await wizard.expectStep('First run')
  await expect(wizard.firstRun.section.getByRole('radio', { name: /^Codex/ })).toHaveAttribute('aria-checked', 'true')
})

test('choosing Enhanced installs the adapter with the agent and seeds the first run', async ({ page, aether }) => {
  const { wizard, id, home } = await toAgentStep(page, aether)
  const agent = wizard.agent
  aether.installStubNpm(id)
  aether.giveCodexLogin(id)

  await agent.setUp('Codex').click()
  await agent.mode('Enhanced').click()
  await expect(agent.mode('Enhanced')).toHaveAttribute('aria-checked', 'true')
  await agent.install('Codex').click()

  const status = agent.status('Codex')
  await expect(status).toContainText('Enhanced installed', { timeout: 5 * 60 * 1000 })
  await expect(status).toContainText('Login found')
  const calls = readFileSync(path.join(home, 'npm-calls.log'), 'utf8').trim().split('\n')
  expect(calls).toHaveLength(2)
  expect(calls[1]).toMatch(/^install -g --prefix \/root\/\.local @agentclientprotocol\/codex-acp@\d+\.\d+\.\d+$/)
  await agent.done().click()

  await agent.continue().click()
  await wizard.expectStep('First run')
  const modes = wizard.firstRun.section.getByRole('radiogroup', { name: 'Mode' })
  await expect(modes.getByRole('radio', { name: /^Enhanced/ })).toHaveAttribute('aria-checked', 'true')
})

test('a failed install shows the command error and its output', async ({ page, aether }) => {
  const { wizard } = await toAgentStep(page, aether)
  const agent = wizard.agent

  // No stub npm: the real install command finds none in the environment.
  await agent.setUp('Codex').click()
  await agent.install('Codex').click()
  const failure = agent.section.getByRole('alert').filter({ hasText: 'Install failed' })
  await expect(failure).toContainText('the install command exited 1', { timeout: 5 * 60 * 1000 })
  await expect(agent.section.locator('[data-slot="code-block"]')).toContainText("npm is not in this environment's PATH")
  await expect(agent.install('Codex')).toBeEnabled()

  // Back closes the setup before it leaves the step.
  await wizard.back().click()
  await wizard.expectStep('Agent')
  await expect(agent.row('Codex')).toContainText('Not installed')
})
