// Real dashboard, gateway, server, Docker exec, native credential setup and
// Git mirrors. Only the external GitHub provider is deterministic: gh's device
// interaction, GitHub's HTTP API and the remote repository transport.
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import type { BrowserContext, Locator, Page } from '@playwright/test'
import type { GitHubOAuthResult, Workspace, WorkspaceMirrorResult } from '../src/lib/types'
import { expect, test, type Member } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

const nativeImage = process.env.AETHER_E2E_STANDARD_IMAGE ?? 'aether-standard:ci'
test.use({ serverOptions: { githubProvider: true, standardImage: nativeImage } })
test.beforeAll(() => {
  // These are normal CI coverage, not an opt-in live-credential suite. A broken
  // provider or unavailable Docker must fail, never silently skip the journeys.
  expect(dockerReachable(), 'GitHub journeys require the CI Docker daemon').toBe(true)
  try {
    execFileSync('docker', ['image', 'inspect', nativeImage], { stdio: 'ignore' })
  } catch {
    throw new Error(`Required GitHub journey image ${nativeImage} is missing. Build it from the repository root: docker build -f images/standard/Dockerfile -t ${nativeImage} .`)
  }
})

async function deviceProvider(context: BrowserContext, home: string) {
  await context.route('https://github.com/login/device', async (route) => {
    if (route.request().method() === 'POST') {
      const code = new URLSearchParams(route.request().postData() ?? '').get('user_code')
      if (code !== 'ABCD-1234') {
        await route.fulfill({ status: 400, body: 'Invalid device code' })
        return
      }
      writeFileSync(path.join(home, 'gh-approved'), '')
      await route.fulfill({ contentType: 'text/html', body: '<h1>Authorization complete</h1>' })
      return
    }
    await route.fulfill({ contentType: 'text/html', body: '<h1>GitHub device authorization</h1><form method="post"><label>Device code<input name="user_code"></label><button>Authorize device</button></form>' })
  })
}

async function approveDevice(page: Page, connection: Locator) {
  await expect(connection).toContainText('ABCD-1234', { timeout: 60_000 })
  await expect(connection.getByRole('link', { name: 'Open GitHub', exact: true })).toHaveAttribute('href', 'https://github.com/login/device')
  const popupPromise = page.context().waitForEvent('page')
  await connection.getByRole('button', { name: 'Copy code and open GitHub' }).click()
  const popup = await popupPromise
  await popup.getByLabel('Device code').fill('ABCD-1234')
  await popup.getByRole('button', { name: 'Authorize device' }).click()
  await expect(popup.getByRole('heading', { name: 'Authorization complete' })).toBeVisible()
  await popup.close()
  await expect(connection).toContainText('Connected as octocat', { timeout: 60_000 })
}

async function screenshot(page: Page, name: string) {
  const file = test.info().outputPath(`${name}.png`)
  await page.screenshot({ path: file, fullPage: true })
  await test.info().attach(name, { path: file, contentType: 'image/png' })
}

async function workspaces(member: Member) {
  return (await member.api.rpc<{ workspaces: Workspace[] }>('workspace.list')).workspaces
}

async function settings(page: Page, member: Member) {
  const url = new URL(member.url)
  url.searchParams.set('page', 'settings')
  await page.goto(url.toString())
  await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible()
}

async function review(page: Page, member: Member, fullName: string, expectedCount: number, branch = 'main') {
  const dialog = page.getByRole('dialog', { name: 'Add GitHub repository' })
  await dialog.getByLabel('Find a repository').fill(fullName)
  await dialog.getByRole('button', { name: `${fullName} · Private`, exact: true }).click()
  // Selecting only edits local form state: no workspace exists until review.
  expect(await workspaces(member)).toHaveLength(expectedCount - 1)
  await dialog.getByRole('button', { name: 'Review repository', exact: true }).click()
  const revision = dialog.getByRole('region', { name: 'Review repository revision' })
  await expect(revision).toBeVisible({ timeout: 60_000 })
  const all = await workspaces(member)
  expect(all).toHaveLength(expectedCount)
  const workspace = all.find((entry) => entry.name === fullName.split('/')[1])!
  expect(workspace).toBeDefined()
  expect(workspace.base_branch).toBe(branch)
  return { dialog, revision, workspace }
}

async function pending(member: Member, workspace: Workspace) {
  const mirror = await member.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: workspace.id })
  expect(mirror.auth).toBe('github')
  expect(mirror.status).toBe('pending')
  expect(mirror.observed_commit).toMatch(/^[a-f0-9]{40}$/)
  expect(mirror.accepted_commit ?? '').toBe('')
  return mirror
}

test('administrator onboards a private read-only repository, then adds another in Settings without logging in again', async ({ page, context, aether }) => {
  const alice = await aether.member('alice')
  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  await wizard.connect.identity.save('Ada Lovelace', 'ada@example.invalid')
  await wizard.connect.continue().click()
  const id = await memberID(alice)
  const home = aether.server.memberHome(id)
  aether.installStubGh(id)
  await deviceProvider(context, home)
  const mutations: { method: string; params: Record<string, unknown> }[] = []
  page.on('request', (request) => {
    const method = request.url().split('/api/v1/')[1]
    if (method === 'workspace.import' || method === 'workspace.mirror.adopt') mutations.push({ method, params: request.postDataJSON() })
  })

  await wizard.repository.button('Add GitHub repository').click()
  const dialog = page.getByRole('dialog', { name: 'Add GitHub repository' })
  const connection = dialog.getByRole('region', { name: 'GitHub connection' })
  await connection.getByRole('button', { name: 'Connect GitHub', exact: true }).click()
  await expect(connection).toContainText('ABCD-1234', { timeout: 60_000 })
  expect(existsSync(path.join(home, '.config/gh/hosts.yml'))).toBe(false)
  await expect(dialog.getByLabel('Find a repository')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: 'Review repository', exact: true })).toBeDisabled()
  await screenshot(page, 'github-onboarding-device-code')
  await approveDevice(page, connection)

  const first = await review(page, alice, 'octocat/first', 1)
  expect(first.workspace.origin ?? '').toBe('')
  const candidate = await pending(alice, first.workspace)
  const upstream = path.join(path.dirname(aether.server.dataDir), 'repos/first')
  expect(candidate.observed_commit).toBe(execFileSync('git', ['-C', upstream, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim())
  await expect(first.revision).toContainText(candidate.observed_commit!)
  await screenshot(page, 'github-onboarding-review-revision')
  await first.dialog.getByRole('button', { name: 'Use repository', exact: true }).click()
  await wizard.expectStep('Agent')
  await expect(page.getByRole('region', { name: 'GitHub connection' })).toContainText('Connected as octocat', { timeout: 60_000 })
  await expect(page.getByRole('button', { name: "I've logged in", exact: true })).toHaveCount(0)
  const accepted = await alice.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: first.workspace.id })
  expect(accepted.status).toBe('ready')
  expect(accepted.accepted_commit).toBe(candidate.observed_commit)
  expect(mutations.filter((entry) => entry.method === 'workspace.mirror.adopt')[0].params).toMatchObject({ workspace_id: first.workspace.id, generation: candidate.generation, expected_commit: candidate.observed_commit })
  await screenshot(page, 'github-onboarding-ready')

  // The native signing setup and credential helper are real; this is not a
  // canned successful connect response from a mocked browser request.
  expect(existsSync(path.join(home, '.ssh/aether_signing'))).toBe(true)
  expect(readFileSync(path.join(home, 'gh-registered-key'), 'utf8')).toBe(readFileSync(path.join(home, '.ssh/aether_signing.pub'), 'utf8'))
  const config = readFileSync(path.join(home, '.gitconfig'), 'utf8')
  expect(config).toContain('helper = !gh auth git-credential')
  expect(config).toContain('signingkey = ~/.ssh/aether_signing')
  expect(config).toContain('gpgsign = true')

  await settings(page, alice)
  await expect(page.getByRole('region', { name: 'GitHub connection' })).toContainText('Connected as octocat', { timeout: 60_000 })
  await page.getByRole('button', { name: 'Add repository', exact: true }).click()
  await page.getByRole('dialog', { name: 'Add GitHub repository' }).getByRole('button', { name: 'Load more', exact: true }).click()
  const second = await review(page, alice, 'team/second', 2, 'trunk')
  expect(second.workspace.origin).toBe('https://github.com/team/second.git')
  const secondCandidate = await pending(alice, second.workspace)
  expect(secondCandidate.branch).toBe('trunk')
  const secondUpstream = path.join(path.dirname(aether.server.dataDir), 'repos/second')
  expect(secondCandidate.observed_commit).toBe(execFileSync('git', ['-C', secondUpstream, 'rev-parse', 'trunk'], { encoding: 'utf8' }).trim())

  // Another administrator client refreshes A -> B while the browser still
  // presents A. Use real Git and the real API, without intercepting adoption.
  writeFileSync(path.join(secondUpstream, 'review-race.txt'), 'New upstream content awaiting administrator review\n')
  execFileSync('git', ['-C', secondUpstream, 'add', 'review-race.txt'])
  execFileSync('git', ['-C', secondUpstream, '-c', 'user.name=Upstream author', '-c', 'user.email=upstream@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-m', 'Advance source during review'])
  const nextCommit = execFileSync('git', ['-C', secondUpstream, 'rev-parse', 'trunk'], { encoding: 'utf8' }).trim()
  expect(nextCommit).not.toBe(secondCandidate.observed_commit)
  const refreshed = await alice.api.rpc<WorkspaceMirrorResult>('workspace.mirror.refresh', { workspace_id: second.workspace.id })
  expect(refreshed.generation).toBe(secondCandidate.generation)
  expect(refreshed.observed_commit).toBe(nextCommit)
  expect(refreshed.accepted_commit ?? '').toBe('')
  await expect(second.revision).toContainText(secondCandidate.observed_commit!)
  await expect(second.revision).not.toContainText(nextCommit)
  const bareRepo = path.join(aether.server.dataDir, 'repos', `${second.workspace.id}.git`)
  const refsBefore = execFileSync('git', ['-C', bareRepo, 'for-each-ref', '--format=%(refname) %(objectname)'], { encoding: 'utf8' })
  await second.dialog.getByRole('button', { name: 'Use repository', exact: true }).click()
  await expect(second.dialog.getByRole('alert').filter({ hasText: 'cas-conflict' })).toBeVisible()
  await expect(second.dialog.getByRole('button', { name: 'Use repository', exact: true })).toBeDisabled()
  await expect(second.revision).toContainText(secondCandidate.observed_commit!)
  await expect(second.revision).not.toContainText(nextCommit)
  expect(mutations.filter((entry) => entry.method === 'workspace.mirror.adopt')[1].params).toMatchObject({
    workspace_id: second.workspace.id, generation: secondCandidate.generation, expected_commit: secondCandidate.observed_commit,
  })
  const retained = await alice.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: second.workspace.id })
  expect(retained.status).toBe('error')
  expect(retained.accepted_commit ?? '').toBe('')
  expect(retained.generation).toBe(secondCandidate.generation)
  expect(retained.observed_commit).toBe(nextCommit)
  expect(execFileSync('git', ['-C', bareRepo, 'for-each-ref', '--format=%(refname) %(objectname)'], { encoding: 'utf8' })).toBe(refsBefore)
  expect(execFileSync('git', ['-C', bareRepo, 'for-each-ref', '--format=%(objectname)', 'refs/heads/trunk'], { encoding: 'utf8' }).trim()).toBe('')
  expect(await workspaces(alice)).toHaveLength(2)
  await screenshot(page, 'github-settings-stale-review-rejected')

  // A deliberate status read reveals B, but never accepts it or navigates.
  await second.dialog.getByRole('button', { name: 'Check source status', exact: true }).click()
  await expect(second.revision).toContainText(nextCommit)
  await expect(second.dialog.getByRole('button', { name: 'Use repository', exact: true })).toBeEnabled()
  expect(mutations.filter((entry) => entry.method === 'workspace.mirror.adopt')).toHaveLength(2)
  const reviewed = await alice.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: second.workspace.id })
  expect(reviewed.observed_commit).toBe(nextCommit)
  expect(reviewed.accepted_commit ?? '').toBe('')
  await second.dialog.getByRole('button', { name: 'Use repository', exact: true }).click()
  await expect(second.dialog).toHaveCount(0)
  const secondAccepted = await alice.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: second.workspace.id })
  expect(secondAccepted.status).toBe('ready')
  expect(secondAccepted.branch).toBe('trunk')
  expect(secondAccepted.accepted_commit).toBe(nextCommit)
  expect(secondAccepted.generation).toBe(secondCandidate.generation)
  expect(execFileSync('git', ['-C', bareRepo, 'rev-parse', 'refs/heads/trunk'], { encoding: 'utf8' }).trim()).toBe(nextCommit)
  expect(mutations.filter((entry) => entry.method === 'workspace.mirror.adopt')[2].params).toMatchObject({
    workspace_id: second.workspace.id, generation: secondCandidate.generation, expected_commit: nextCommit,
  })
  expect(new URL(page.url()).searchParams.get('page')).toBe('workspace')
  expect(new URL(page.url()).searchParams.get('id')).toBe(second.workspace.id)
  expect(mutations.filter((entry) => entry.method === 'workspace.import')).toHaveLength(2)
  const calls = readFileSync(path.join(home, 'gh-calls.log'), 'utf8').split('\n')
  expect(calls.filter((call) => call.startsWith('auth login '))).toHaveLength(1)
  await screenshot(page, 'github-settings-additional-repository-ready')
})

test('cancelled and rejected device authorization recover, and a failed fetch retries the retained workspace', async ({ page, context, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: 'Alice' })
  const id = await memberID(alice)
  const home = aether.server.memberHome(id)
  aether.installStubGh(id)
  await deviceProvider(context, home)
  await settings(page, alice)
  const connection = page.getByRole('region', { name: 'GitHub connection' })
  await connection.getByRole('button', { name: 'Connect GitHub', exact: true }).click()
  await expect(connection).toContainText('ABCD-1234', { timeout: 60_000 })
  const original = await alice.api.rpc<GitHubOAuthResult>('github.oauth.status')
  await connection.getByRole('button', { name: 'Cancel connection', exact: true }).click()
  await expect(connection).toContainText('GitHub connection cancelled.')
  expect(existsSync(path.join(home, '.config/gh/hosts.yml'))).toBe(false)
  await connection.getByRole('button', { name: 'Connect GitHub', exact: true }).click()
  await expect(connection).toContainText('ABCD-1234', { timeout: 60_000 })
  const restarted = await alice.api.rpc<GitHubOAuthResult>('github.oauth.status')
  expect(restarted.session_id).not.toBe(original.session_id)
  writeFileSync(path.join(home, 'gh-denied'), '')
  await expect(connection).toContainText('GitHub connection failed.', { timeout: 60_000 })
  await expect(connection.getByRole('alert')).toContainText('GitHub device authorization denied')
  expect(existsSync(path.join(home, '.config/gh/hosts.yml'))).toBe(false)
  rmSync(path.join(home, 'gh-denied'))
  await connection.getByRole('button', { name: 'Connect GitHub', exact: true }).click()
  await approveDevice(page, connection)
  await page.getByRole('button', { name: 'Add repository', exact: true }).click()
  const failFetch = path.join(path.dirname(aether.server.dataDir), 'fail-fetch')
  writeFileSync(failFetch, '')
  const retained = await review(page, alice, 'octocat/first', 1)
  await expect(retained.revision).toContainText('No revision fetched yet')
  await expect(retained.dialog.getByRole('button', { name: 'Use repository', exact: true })).toBeDisabled()
  await expect(retained.dialog.getByRole('button', { name: 'Review repository', exact: true })).toHaveCount(0)
  rmSync(failFetch)
  await retained.dialog.getByRole('button', { name: 'Retry fetch', exact: true }).click()
  await expect(retained.dialog.getByRole('button', { name: 'Use repository', exact: true })).toBeEnabled({ timeout: 60_000 })
  expect((await workspaces(alice)).map((entry) => entry.id)).toEqual([retained.workspace.id])
  const candidate = await pending(alice, retained.workspace)
  await expect(retained.revision).toContainText(candidate.observed_commit!)
  await retained.dialog.getByRole('button', { name: 'Use repository', exact: true }).click()
  await expect(retained.dialog).toHaveCount(0)
  const ready = await alice.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: retained.workspace.id })
  expect(ready.accepted_commit).toBe(candidate.observed_commit)
  await screenshot(page, 'github-cancellation-and-fetch-recovered')
})

test('collaborators have no repository connection controls and forged administrator RPCs are denied', async ({ page, aether }) => {
  const admin = await aether.member('admin')
  await admin.api.local('link.apply', { addr: aether.server.addr, name: 'Admin' })
  const bob = await aether.member('bob')
  const wizard = await OnboardingWizard.open(page, bob.url)
  await wizard.connect.link(aether.server.addr, { invite: await aether.invite(admin), name: 'Bob' })
  await expect(wizard.connect.section).toContainText('(collaborator)')
  await wizard.connect.continue().click()
  await wizard.expectStep('Repository')
  await expect(wizard.repository.button('Add GitHub repository')).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'GitHub connection' })).toHaveCount(0)
  await settings(page, bob)
  await expect(page.getByRole('region', { name: 'GitHub connection' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Add repository', exact: true })).toHaveCount(0)
  for (const method of ['github.oauth.start', 'github.oauth.status', 'github.oauth.cancel', 'github.repositories.list']) {
    await expect(bob.api.rpc(method, { session_id: 'forged' })).rejects.toThrow(/admin|permission|forbidden/i)
  }
  await expect(bob.api.rpc('workspace.import', { name: 'forged', source_url: 'https://github.com/octocat/first.git', branch: 'main', auth: 'github', github_account_id: 42 })).rejects.toThrow(/admin|permission|forbidden/i)
  expect(await workspaces(admin)).toHaveLength(0)
  await screenshot(page, 'github-collaborator-settings')
})
