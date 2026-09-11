// The mobile file browser must return to its tree after viewing one file, so
// another file can be opened without losing either pane. Every control here
// is reached by tap: the phone has no mouse, and a control that only answers
// mouse events would still pass a click-driven test.

import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test('mobile Files returns from one viewer to the repository tree', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  // seedRepo provides two committed text files: README.md and agent.sh.
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  await page.goto(alice.url)

  // At a phone width the real shell starts with its mobile sidebar rail, and
  // the drawer it opens closes itself on the navigation it makes.
  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  await page
    .getByRole('dialog', { name: 'Runs' })
    .getByRole('button', { name: 'Files', exact: true })
    .tap()
  await expect(page.getByRole('dialog', { name: 'Runs' })).toBeHidden()

  await expect(page.getByRole('heading', { name: 'Files', exact: true })).toBeVisible()
  const tree = page.getByRole('complementary', { name: 'Files' })
  const readme = tree.getByRole('button', { name: 'README.md', exact: true })
  await expect(readme).toBeVisible()
  await readme.tap()

  const viewer = page.getByRole('article')
  await expect(viewer.locator('header')).toContainText('README.md')
  await expect(viewer.locator('.cm-content')).toContainText('# project')
  await viewer.getByRole('button', { name: 'Browse', exact: true }).tap()
  await expect(tree).toBeVisible()
  await expect(viewer).toBeHidden()

  const agent = tree.getByRole('button', { name: 'agent.sh', exact: true })
  await expect(agent).toBeVisible()
  await agent.tap()
  await expect(viewer.locator('header')).toContainText('agent.sh')
  await expect(viewer.locator('.cm-content')).toContainText('echo agent-ready')
})
