// Only a real browser hit-tests the card's navigation overlay against its
// selectable branch text and copy control.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a card gives up its branch name without opening the run', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const { run } = await alice.api.rpc<{ run: { id: string; branch: string } }>(
    'run.launch',
    {
      workspace_id: workspaces[0].id,
      harness: 'fake',
      task: 'a run whose branch name is long enough to be cut short on a card',
      mode: 'headless',
    },
  )

  await page.goto(alice.url)
  const card = page.getByRole('article').filter({ hasText: 'long enough' })
  await expect(card).toBeVisible()
  await card.getByRole('button', { name: /^Show details for / }).click()
  const name = card.getByText(run.branch, { exact: true })

  await name.dblclick({ position: { x: 4, y: 4 } })
  const selected = await page.evaluate(
    () => window.getSelection()?.toString().trim() ?? '',
  )
  expect(selected).toBe(run.branch.split('/')[0])

  // Reaching for the branch is not a way into the run: both the name and the
  // copy control sit above the overlay. The toast is waited for first, so
  // the click is known to have reached the control and the board has had the
  // time a navigation would have needed to land.
  await card.getByRole('button', { name: `Copy branch ${run.branch}` }).click()
  await expect(page.getByText(/Copied|Press Ctrl\+C to copy/)).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Run tabs' })).toHaveCount(0)
  await expect(card).toBeVisible()
})
