// A session title longer than the sidebar is cut off with an ellipsis, and
// nothing in its row - including the screen-reader timestamp positioned
// after the title - widens the session list. Only layout in a real browser
// shows the list's scroll width.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

const task = 'Investigate why the session list scrolls sideways when a title is far longer than the sidebar can show'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

test('a long session title does not let the session list scroll sideways', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  await alice.api.rpc('run.launch', { workspace_id: workspaces[0].id, harness: 'fake', task, mode: 'headless' })

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  const sessions = page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Sessions' })
  await expect(sessions.getByRole('button', { name: new RegExp(task) })).toBeVisible({ timeout: 30_000 })

  const { scrollWidth, clientWidth } = await sessions.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  expect(scrollWidth).toBeLessThanOrEqual(clientWidth)
})
