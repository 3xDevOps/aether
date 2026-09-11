// The mobile file browser must return to its tree after viewing one file, so
// another file can be opened without losing either pane.

import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

test('mobile Files returns from one viewer to the repository tree', async ({
  page,
  aether,
}) => {
  await page.setViewportSize({ width: 390, height: 844 })

  const alice = await aether.member('alice')
  // seedRepo provides two committed text files: README.md and agent.sh.
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  await page.goto(alice.url)

  // On a 390px viewport the real shell starts with its mobile sidebar rail.
  await page.getByRole('button', { name: 'Expand sidebar' }).click()
  await page
    .getByRole('navigation', { name: 'Surfaces' })
    .getByRole('button', { name: 'Files', exact: true })
    .click()
  await page.getByRole('button', { name: 'Collapse sidebar' }).click()

  await expect(page.getByRole('heading', { name: 'Files', exact: true })).toBeVisible()
  const tree = page.getByRole('complementary', { name: 'Files' })
  const readme = tree.getByRole('button', { name: 'README.md', exact: true })
  await expect(readme).toBeVisible()
  await readme.click()

  const viewer = page.getByRole('article')
  await expect(viewer.locator('header')).toContainText('README.md')
  await expect(viewer.locator('.cm-content')).toContainText('# project')
  await viewer.getByRole('button', { name: 'Browse', exact: true }).click()
  await expect(tree).toBeVisible()
  await expect(viewer).toBeHidden()

  const agent = tree.getByRole('button', { name: 'agent.sh', exact: true })
  await expect(agent).toBeVisible()
  await agent.click()
  await expect(viewer.locator('header')).toContainText('agent.sh')
  await expect(viewer.locator('.cm-content')).toContainText('echo agent-ready')
})
