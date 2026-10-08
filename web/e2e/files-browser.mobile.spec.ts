// The mobile file browser keeps its tree in a side sheet once a file is open,
// so another file can be opened without losing the editor. Every control here
// is reached by tap: the phone has no mouse, and a control that only answers
// mouse events would still pass a click-driven test.

import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test('mobile Files opens a second file from the tree sheet', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  // seedRepo provides two committed text files: README.md and agent.sh.
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  await page.goto(alice.url)

  await page.getByRole('button', { name: 'More navigation' }).tap()
  await page.getByRole('menuitem', { name: 'Files', exact: true }).tap()
  await expect(page.getByRole('menu')).toBeHidden()

  await expect(page.getByRole('heading', { name: 'Files', exact: true })).toBeAttached()
  const tree = page.getByRole('complementary', { name: 'Files' })
  const readme = tree.getByRole('button', { name: 'README.md', exact: true })
  await expect(readme).toBeVisible()
  expect((await readme.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(40)
  await readme.tap()

  const viewer = page.getByRole('article')
  await expect(viewer.locator('header')).toContainText('README.md')
  await expect(viewer.locator('.cm-content')).toContainText('# project')
  await expect(tree).toBeHidden()
  const browse = viewer.getByRole('button', { name: 'Browse', exact: true })
  expect((await browse.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(40)
  await browse.tap()

  const sheet = page.getByRole('dialog', { name: 'Files' })
  const agent = sheet.getByRole('button', { name: 'agent.sh', exact: true })
  await expect(agent).toBeVisible()
  await agent.tap()
  await expect(sheet).toBeHidden()
  await expect(viewer.locator('header')).toContainText('agent.sh')
  await expect(viewer.locator('.cm-content')).toContainText('echo agent-ready')
})
