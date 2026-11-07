// One member completes and deletes session notes as stickies in the Chat
// view, and the completed one keeps showing in Details' Done history.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

test('completing and deleting a sticky note', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1920, height: 1080 })
  const alice = await aether.member('Alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'sticky note demo',
  })
  await page.goto(`${alice.url}&run=${run.id}&view=session`)
  await expect(page.getByRole('heading', { name: 'sticky note demo', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Hide details', exact: true }).click()

  const post = (body: string, idempotencyKey: string) =>
    alice.api.rpc('run.room.post', {
      workspace_id: workspaces[0].id,
      run_id: run.id,
      kind: 'comment',
      body,
      idempotency_key: idempotencyKey,
    })
  await post('finish this one', 'e2e-sticky-done')
  await post('delete this one', 'e2e-sticky-delete')

  const rail = page.getByRole('list', { name: 'Notes' })
  await expect(rail.getByText('finish this one', { exact: true })).toBeVisible()
  await expect(rail.getByText('delete this one', { exact: true })).toBeVisible()
  await testInfo.attach('sticky-rail-open', { body: await page.screenshot(), contentType: 'image/png' })

  const doneCard = rail.locator('li').filter({ hasText: 'finish this one' })
  await doneCard.hover()
  await doneCard.getByRole('button', { name: 'Complete note', exact: true }).click()
  await expect(rail.getByText('finish this one', { exact: true })).toBeHidden()

  const deleteCard = rail.locator('li').filter({ hasText: 'delete this one' })
  await deleteCard.hover()
  await deleteCard.getByRole('button', { name: 'Delete note', exact: true }).click()
  await expect(rail.getByText('delete this one', { exact: true })).toBeHidden()
  await testInfo.attach('sticky-rail-settled', { body: await page.screenshot(), contentType: 'image/png' })

  await page.getByRole('button', { name: 'Show details', exact: true }).click()
  const details = page.getByRole('complementary', { name: 'Session details' })
  const done = details.getByRole('region', { name: 'Done' })
  await expect(done.getByText('finish this one', { exact: true })).toBeVisible()
  await expect(details.getByText('delete this one', { exact: true })).toHaveCount(0)
})
