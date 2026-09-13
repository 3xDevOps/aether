// The `aether://run/<id>` deep link, from the dashboard's side. Both shells
// turn that link into `<dashboard>?run=<id>` and load it, so the query is the
// whole contract - and only a real browser against a real gateway shows that
// the id survives the token exchange, the first hydration and the reload
// that must not act on it twice.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

const task = 'the run the link names'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a run deep link opens that run once', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task,
    mode: 'headless',
  })

  // What the desktop shell loads: the gateway's own tokened URL with the run
  // id appended. The Android shell appends the same parameter to the
  // server-hosted dashboard, which carries no token.
  await page.goto(`${alice.url}&run=${run.id}`)

  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  // Neither parameter is left where a reload would reuse it.
  expect(new URL(page.url()).search).toBe('')

  await page.reload()
  await expect(page.getByRole('heading', { name: 'Board', exact: true })).toBeVisible()
})
