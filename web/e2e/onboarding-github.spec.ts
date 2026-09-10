// The Agents step's Connect GitHub screen. The login itself is a device
// flow only a person can finish, so a stub `gh` in the member's environment
// home stands in for it; everything the server does around that stub - the
// signing key, the git identity written next to gh's own credential block,
// and the key registered on the account - is real.

import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'

import { githubLoginCommand } from '@/lib/github'

import { expect, test } from './fixtures'
import { dockerReachable, standardImage } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

test.skip(!dockerReachable(), 'connecting GitHub needs a reachable Docker daemon')

test('connecting GitHub registers a signing key and keeps gh credentials', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  // The identity is what the connect writes into the home's .gitconfig, so
  // this scenario sets one rather than skipping the step.
  await wizard.gitIdentity.save('Ada Lovelace', 'ada@example.invalid')
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')

  const id = await memberID(alice)
  const home = aether.server.memberHome(id)

  // This environment has no gh, which is the state every environment from
  // before the standard image shipped one is in. The screen probes the
  // running container and names both halves of the remedy rather than
  // showing a login that container cannot run. Opening the dock starts a
  // real container, so the first answer is what to wait on: the button is
  // clickable long before the container exists.
  const github = wizard.agents.github
  await wizard.agents.connectGitHub().click()
  await expect(github.section).toContainText(
    'There is no gh in your environment terminal',
    { timeout: 3 * 60 * 1000 },
  )

  await expect(github.commands).toContainText([
    `docker pull ${standardImage}`,
    'aether terminal stop',
  ])
  await expect(github.section).not.toContainText(githubLoginCommand)
  await wizard.back().click()
  await wizard.expectStep('Agents')

  // The container is the same one; the stub reaches it through the bind
  // mounted home, so reopening the screen is enough to probe again. The
  // server bounds the probe at twenty seconds, well inside this.
  aether.installStubGh(id)
  await wizard.agents.connectGitHub().click()
  // The state, not the command block: a probe that threw would put the
  // same block back, so only this sentence proves the check passed.
  await expect(github.section).toContainText(
    'The login command is ready in your environment terminal:',
    { timeout: 30_000 },
  )
  await expect(github.commands).toContainText([githubLoginCommand])

  await github.confirmLoggedIn().click()
  await expect(github.section).toContainText('Connected to GitHub as octocat', {
    timeout: 60_000,
  })
  await expect(github.section).toContainText(/Signing key SHA256:/)

  // The key stays on the server: the private half in the member home, the
  // public half registered on the account through gh.
  expect(existsSync(path.join(home, '.ssh', 'aether_signing'))).toBe(true)
  const publicKey = readFileSync(path.join(home, '.ssh', 'aether_signing.pub'), 'utf8')
  expect(readFileSync(path.join(home, 'gh-registered-key'), 'utf8')).toBe(publicKey)

  // git in that home now has both halves: gh's credential helper for the
  // push, and the signing settings for the commit.
  const gitconfig = readFileSync(path.join(home, '.gitconfig'), 'utf8')
  expect(gitconfig).toContain('helper = !gh auth git-credential')
  expect(gitconfig).toContain('format = ssh')
  expect(gitconfig).toContain('signingkey = ~/.ssh/aether_signing')
  expect(gitconfig).toContain('gpgsign = true')
  const calls = readFileSync(path.join(home, 'gh-calls.log'), 'utf8')
  // The dock typed the login into the container, which is the half of
  // this flow no server call can stand in for.
  expect(calls).toContain(
    'auth login --hostname github.com --git-protocol https --web --scopes admin:ssh_signing_key',
  )
  expect(calls).toContain('auth setup-git --hostname github.com')
  // The server reads the account's keys back after registering, so the
  // fingerprint it shows is the one GitHub holds.
  expect(calls).toContain('ssh-key list')

  // Back closes the sub-screen without leaving the step, and the step now
  // says who it connected as.
  await wizard.back().click()
  await wizard.expectStep('Agents')
  await expect(github.section).toContainText('Connected in this session as octocat')
  await expect(wizard.agents.connectGitHub()).toBeVisible()
})
