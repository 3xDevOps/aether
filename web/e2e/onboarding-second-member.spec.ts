import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import type { RepoPushResult, WorkspaceMirrorResult } from '../src/lib/types'
import { expect, test } from './fixtures'
import type { WorkspaceResult } from './harness/client'
import { seedWorkspace } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

const exec = promisify(execFile)

test('a collaborator seeds an empty local-only workspace without mirror management', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: 'Alice' })
  const { workspace } = await alice.api.rpc<WorkspaceResult>('workspace.add', {
    name: 'project', base_branch: 'main', environment: {},
  })
  const repo = await aether.seedRepo('project')
  const code = await aether.invite(alice)

  const bob = await aether.member('bob')
  const clone = await aether.cloneRepo(repo, 'project-clone')
  const commit = (await exec('git', ['-C', clone, 'rev-parse', 'HEAD'])).stdout.trim()
  const wizard = await OnboardingWizard.open(page, bob.url)

  await wizard.link.link(aether.server.addr, { invite: code, name: 'Bob' })
  await expect(wizard.link.section).toContainText('(collaborator)')
  await wizard.link.continue().click()
  // The git identity is optional and this scenario is not about it.
  await wizard.gitIdentity.skip().click()

  // The workspace is already there, so this step picks rather than creates.
  await wizard.expectStep('Workspace')
  await wizard.workspace.use('project').click()

  await wizard.expectStep('Repository')
  await expect(wizard.repository.section.getByRole('status', { name: 'Source mirror status' })).toContainText('Local-only workspace.')
  await expect(wizard.repository.section.getByRole('button', { name: /source mirror/ })).toHaveCount(0)
  await wizard.repository.localClone().click()
  await wizard.repository.addRemote(clone)
  await expect(wizard.repository.section.getByLabel('Push command', { exact: true })).toHaveValue('git push -u aether main')

  const pushedResponse = page.waitForResponse((response) =>
    response.url().endsWith('/local/v1/repo.push') && response.request().method() === 'POST',
  )
  await wizard.repository.push().click()
  const response = await pushedResponse
  expect(response.ok()).toBe(true)
  const pushed = await response.json() as RepoPushResult
  expect(pushed.state).toBe('pushed')
  expect(pushed.local_commit).toBe(commit)
  expect(pushed.workspace_commit).toBe('')
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await expect(wizard.repository.gitOutput()).toContainText('[new branch]      main -> main')

  // Recompare through the real gateway: the server now has this exact base,
  // rather than merely trusting that the UI displayed a successful response.
  const current = await bob.api.local<RepoPushResult>('repo.push', { workspace_id: workspace.id })
  expect(current.state).toBe('up-to-date')
  expect(current.workspace_commit).toBe(commit)
  await expect(wizard.repository.section.getByRole('button', { name: /source mirror/ })).toHaveCount(0)
  await page.screenshot({ path: test.info().outputPath('collaborator-local-only-pushed.png'), fullPage: true })
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')
})

test('a collaborator links a mirrored workspace without pushing or managing its source', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspaceID = workspaces[0].id
  // Configure real server ownership without fetching a public service.
  const sourceURL = 'https://github.com/aether-e2e/source-ownership.git'
  const source = await alice.api.rpc<WorkspaceMirrorResult>('workspace.mirror.configure', {
    workspace_id: workspaceID, source_url: sourceURL, branch: 'main', auth: 'public',
  })
  expect(source.enabled).toBe(true)
  const code = await aether.invite(alice)
  const bob = await aether.member('bob')
  const clone = await aether.cloneRepo(repo, 'project-clone')
  const pushes: string[] = []
  page.on('request', (request) => {
    if (request.url().endsWith('/local/v1/repo.push')) pushes.push(request.url())
  })
  const wizard = await OnboardingWizard.open(page, bob.url)

  await wizard.link.link(aether.server.addr, { invite: code, name: 'Bob' })
  await expect(wizard.link.section).toContainText('(collaborator)')
  await wizard.link.continue().click()
  await wizard.gitIdentity.skip().click()
  await wizard.expectStep('Workspace')
  await wizard.workspace.use('project').click()

  await wizard.expectStep('Repository')
  const status = wizard.repository.section.getByRole('status', { name: 'Source mirror status' })
  await expect(status).toContainText('Source mirror configured.')
  await expect(status).toContainText(sourceURL)
  await expect(wizard.repository.section.getByRole('button', { name: /source mirror/ })).toHaveCount(0)
  await wizard.repository.localClone().click()
  await wizard.repository.addRemote(clone)
  await expect(wizard.repository.section).toContainText(`Connected ${clone}`)
  await expect(wizard.repository.continue()).toBeEnabled()
  await expect(wizard.repository.push()).toHaveCount(0)
  await expect(wizard.repository.section.getByLabel('Push command', { exact: true })).toHaveCount(0)
  await page.screenshot({ path: test.info().outputPath('collaborator-mirrored-link-only.png'), fullPage: true })
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')
  expect(pushes).toEqual([])
  const current = await bob.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: workspaceID })
  expect(current.enabled).toBe(true)
  expect(current.source_url).toBe(sourceURL)
  expect(current.status).toBe('pending')
  expect(current.generation).toBe(source.generation)
  expect(current.accepted_commit ?? '').toBe('')
})
