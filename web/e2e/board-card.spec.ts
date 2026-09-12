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

  // A double click takes a whole word out of the branch name itself, not
  // just any text the page happens to have selected. Aimed at the start of
  // the name so the word is a known one: run branches are
  // aether/run-<slug>-<id>, and the name is truncated, so which word sits
  // under the middle of the chip depends on how wide the card is today.
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
