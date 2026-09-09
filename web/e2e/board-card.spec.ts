// What a board card lets you do with a run's branch name. The card is one
// click target, so an element that is not deliberately raised above the
// overlay is unreachable: its text cannot be selected and its `title` never
// resolves, because a native tooltip walks the ancestors of whatever the
// pointer actually hit. Only a real browser hit-tests, so this cannot be
// checked in jsdom.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a card gives up its branch name', async ({ page, aether }) => {
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
    },
  )

  await page.goto(alice.url)
  const card = page.getByRole('article').filter({ hasText: 'long enough' })
  await expect(card).toBeVisible()
  const name = card.getByTitle(run.branch)

  // The tooltip chain, resolved the way the browser resolves it: from the
  // element under the pointer upwards. Before the chip was raised this
  // answered null, because the pointer landed on the card's click overlay.
  const tooltip = await name.evaluate((el) => {
    const box = el.getBoundingClientRect()
    let node = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)
    while (node) {
      const title = node.getAttribute('title')
      if (title) return title
      node = node.parentElement
    }
    return null
  })
  expect(tooltip).toBe(run.branch)

  await name.dblclick()
  // A double click takes a word out of the branch name itself, not just any
  // text the page happens to have selected.
  const selected = await page.evaluate(
    () => window.getSelection()?.toString().trim() ?? '',
  )
  expect(selected).not.toBe('')
  expect(run.branch).toContain(selected)

})
