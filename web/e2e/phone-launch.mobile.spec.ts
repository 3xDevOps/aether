// Launching from a phone against a real server: + New opens a bottom sheet
// whose repository and agent default to your last session's, and a shared
// prompt opens it filled in without launching.

import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

const idleAgent = 'sleep 600'

test('+ New launches a task on the last session\'s repository and agent', async ({ page, aether }) => {
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

  await page.goto(alice.url)
  const tabs = page.getByRole('navigation', { name: 'Main navigation' })
  await tabs.getByRole('button', { name: 'New session' }).tap()
  const sheet = page.getByRole('dialog', { name: 'New session' })
  await expect(sheet.getByRole('combobox', { name: 'Repository' })).toHaveText('docs')
  await expect(sheet.getByRole('combobox', { name: 'Agent' })).toHaveText('Codex')
  await sheet.getByLabel('Task').fill('write the changelog')
  await sheet.getByRole('button', { name: 'Launch', exact: true }).tap()
  await expect(sheet).toBeHidden()

  const { runs } = await alice.api.rpc<{ runs: { task: string; workspace_id: string; harness: string }[] }>('run.list', {})
  expect(runs.find((run) => run.task === 'write the changelog')).toMatchObject({ workspace_id: docs.id, harness: 'codex' })

})

test('a shared prompt fills + New without launching and leaves the address', async ({ page, aether }) => {
  await page.setViewportSize({ width: 360, height: 760 })
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'))
  const url = new URL(alice.url)
  url.searchParams.set('new', '1')
  url.searchParams.set('prompt', 'hi')
  await page.goto(url.toString())

  const sheet = page.getByRole('dialog', { name: 'New session' })
  await expect(sheet.getByLabel('Task')).toHaveValue('hi')
  await expect.poll(() => new URL(page.url()).searchParams.has('prompt')).toBe(false)
  expect(new URL(page.url()).searchParams.has('new')).toBe(false)
  const { runs } = await alice.api.rpc<{ runs: unknown[] | null }>('run.list', {})
  expect(runs ?? []).toHaveLength(0)

  await page.reload()
  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible()
  await expect(sheet).toBeHidden()
})
