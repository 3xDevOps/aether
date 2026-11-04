// Launching from a phone against a real server: + New opens a bottom sheet
// on your last session's repository, not the active one, with your saved
// agent, and a shared prompt fills it in without launching.

import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

const idleAgent = 'sleep 600'

test('+ New launches on the last session\'s repository with the saved agent', async ({ page, aether }) => {
  test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')
  await page.setViewportSize({ width: 360, height: 760 })
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'), 'shop')
  const { workspace: docs } = await alice.api.rpc<{ workspace: { id: string } }>('workspace.add', { name: 'docs', base_branch: 'main', environment: {} })
  await alice.api.local('link.repo', { repo: await aether.seedRepo('docs'), workspace_id: docs.id })
  await alice.api.local('repo.push', { workspace_id: docs.id })
  const id = await memberID(alice)
  aether.installAgent(id, 'claude', idleAgent)
  aether.installAgent(id, 'codex', idleAgent)
  await alice.api.rpc('run.launch', { workspace_id: docs.id, harness: 'codex', task: 'an earlier session' })
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string; name: string }[] }>('workspace.list')
  await alice.api.local('workspace.selection', { workspace_id: workspaces.find((w) => w.name === 'shop')!.id })
  const { revision } = await alice.api.rpc<{ revision: number }>('launch.defaults.get')
  await alice.api.rpc('launch.defaults.set', { defaults: { agent: 'claude' }, revision })

  await page.goto(alice.url)
  const tabs = page.getByRole('navigation', { name: 'Main navigation' })
  await tabs.getByRole('button', { name: 'New session' }).tap()
  const sheet = page.getByRole('dialog', { name: 'New session' })
  await expect(sheet.getByRole('combobox', { name: 'Repository' })).toHaveText('docs')
  await expect(sheet.getByRole('combobox', { name: 'Agent' })).toHaveText('Claude Code')
  await expect(sheet.getByText('Repository from your last session.')).toBeVisible()
  await sheet.getByLabel('Task').fill('write the changelog')
  await sheet.getByRole('button', { name: 'Launch', exact: true }).tap()
  await expect(sheet).toBeHidden()

  const { runs } = await alice.api.rpc<{ runs: { task: string; workspace_id: string; harness: string }[] }>('run.list', {})
  expect(runs.find((run) => run.task === 'write the changelog')).toMatchObject({ workspace_id: docs.id, harness: 'claude' })

})

test('a shared prompt fills + New without launching and leaves the address', async ({ page, aether }) => {
  await page.setViewportSize({ width: 360, height: 760 })
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'))
  aether.installAgent(await memberID(alice), 'claude')
  const url = new URL(alice.url)
  url.searchParams.set('new', '1')
  url.searchParams.set('prompt', 'hi')
  await page.goto(url.toString())

  const sheet = page.getByRole('dialog', { name: 'New session' })
  await expect(sheet.getByLabel('Task')).toHaveValue('hi')
  await expect(sheet.getByRole('status').filter({ hasText: 'Shared from another app' })).toContainText('2 characters, 1 line')
  // Launch could start a session now; none starts until it is tapped.
  await expect(sheet.getByRole('button', { name: 'Launch', exact: true })).toBeEnabled()
  await page.waitForTimeout(2000)
  await expect.poll(() => new URL(page.url()).searchParams.has('prompt')).toBe(false)
  expect(new URL(page.url()).searchParams.has('new')).toBe(false)
  const { runs } = await alice.api.rpc<{ runs: unknown[] | null }>('run.list', {})
  expect(runs ?? []).toHaveLength(0)

  await page.reload()
  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible()
  await expect(sheet).toBeHidden()
})
