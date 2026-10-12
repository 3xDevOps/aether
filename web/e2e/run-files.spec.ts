// A run's files from inside the run. A path the agent printed is a link only
// when the checkout has that file; following it opens the run's Files view at
// the line; an edit saves into the checkout; and the editor outlives both the
// reread its own save causes and a visit to another view. Only a real
// terminal, a real checkout and the real event stream show all of that.

import type { Locator, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

const task = 'read the files'
// What a compiler prints: a path, a line and a message. seedRepo commits README.md.
// Redrawn each second so output printed before the terminal attached is not the only copy.
const agent = `while :; do
  printf '\\033[2J\\033[H'
  printf '%s\\r\\n' 'README.md:1: heading' 'missing.md:3: gone'
  sleep 1
done`

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

/** xterm asks for a line's links when the pointer reaches the line and applies
 * the answer on the next move, so this moves twice before it clicks. */
async function clickOutput(page: Page, rows: Locator, text: string) {
  const box = (await rows.locator('> div').filter({ hasText: text }).first().boundingBox())!
  const y = box.y + box.height / 2
  await page.mouse.move(box.x + 12, y)
  await page.mouse.move(box.x + 16, y)
  await page.mouse.click(box.x + 16, y)
}

test('a path in the terminal opens that file in the run at its line, and an edit saves', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', agent)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task,
  })
  const read = async () =>
    (await alice.api.rpc<{ content: string }>('files.read', { workspace_id: workspaces[0].id, run_id: run.id, path: 'README.md' })).content

  await page.goto(`${alice.url}&run=${run.id}`)
  const views = page.getByRole('tablist', { name: 'Run views' })
  const tab = (name: string) => views.getByRole('tab', { name, exact: true })
  const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *)')
  await expect(rows).toContainText('missing.md:3: gone', { timeout: 3 * 60 * 1000 })

  await expect(async () => {
    await clickOutput(page, rows, 'README.md:1')
    await expect(tab('Files')).toHaveAttribute('aria-selected', 'true', { timeout: 2000 })
  }).toPass({ timeout: 60_000 })
  expect(new URL(page.url()).search).toBe(`?run=${run.id}&view=files`)

  const viewer = page.getByRole('article')
  await expect(viewer.locator('header')).toContainText('README.md')
  await expect(viewer.locator('.cm-marked-line')).toHaveText('# project')
  const tree = page.getByRole('complementary', { name: 'Files' })
  await expect(tree.getByRole('button', { name: 'README.md', exact: true })).toHaveAttribute('aria-current', 'true')

  const editor = viewer.locator('.cm-editor')
  await editor.evaluate((node) => node.setAttribute('data-kept', 'yes'))
  await viewer.locator('.cm-content').click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.type('edited in the run')
  await viewer.getByRole('button', { name: 'Save', exact: true }).click()
  await expect.poll(read).toBe('# project\nedited in the run')

  // The save changes the checkout, which drops what the dashboard had read of
  // it. The mark on the tree row says that happened; the editor is the same one.
  await expect(tree.getByRole('img', { name: 'Changed in this run' })).toBeVisible({ timeout: 60_000 })
  await expect(editor).toHaveAttribute('data-kept', 'yes')
  await expect(editor).toContainText('edited in the run')
  await expect(viewer.getByRole('button', { name: 'Save', exact: true })).toBeDisabled()

  await tab('Changes').click()
  await page.getByRole('region', { name: 'README.md' }).getByRole('button', { name: 'README.md', exact: true }).click()
  await expect(tab('Files')).toHaveAttribute('aria-selected', 'true')
  await expect(editor).toHaveAttribute('data-kept', 'yes')

  // The checkout has no missing.md, so that path is text: clicking it leaves the terminal up.
  await tab('Terminal').click()
  await clickOutput(page, rows, 'missing.md:3')
  await clickOutput(page, rows, 'missing.md:3')
  await expect(tab('Terminal')).toHaveAttribute('aria-selected', 'true')
})
