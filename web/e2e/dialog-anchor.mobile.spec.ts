import { seedWorkspace } from './harness/setup'
import { expect, shrinkToKeyboardHeight, test } from './mobile'

test('a confirm opens as a sheet along the bottom of a phone screen', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  // A short dialog is the only kind that can tell a sheet from a centred box.
  await alice.api.rpc('template.save', {
    workspace_id: workspaces[0].id,
    name: 'nightly',
    task: 'run the nightly sweep',
    harness: 'fake',
  })

  await page.goto(alice.url)
  await page.getByRole('button', { name: /^Open sidebar/ }).tap()
  await page
    .getByRole('dialog', { name: 'Aether' })
    .getByRole('button', { name: 'Templates', exact: true })
    .tap()
  await page.getByRole('button', { name: 'More for nightly' }).tap()
  await page.getByRole('menuitem', { name: 'Delete' }).tap()

  const confirm = page.getByRole('alertdialog')
  await expect(confirm).toBeVisible()
  const viewport = await page.evaluate(() => ({ width: window.innerWidth, height: window.innerHeight }))
  await expect
    .poll(async () => {
      const box = await confirm.boundingBox()
      return box ? Math.round(box.y + box.height) : -1
    })
    .toBe(viewport.height)
  const box = await confirm.boundingBox()
  if (!box) throw new Error('the confirm did not render')
  expect(Math.round(box.width)).toBe(viewport.width)
  expect(box.y).toBeGreaterThan(viewport.height / 2)
})

test('the launch form keeps its footer on screen with the keyboard up', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  await page.goto(alice.url)
  await shrinkToKeyboardHeight(page)

  await page
    .getByRole('banner')
    .getByRole('button', { name: 'New run' })
    .tap()

  const dialog = page.getByRole('dialog', { name: 'New run' })
  await expect(dialog).toBeVisible()

  const height = await page.evaluate(() => window.innerHeight)
  await expect
    .poll(async () => {
      const box = await dialog.boundingBox()
      return box ? Math.round(box.y + box.height) : -1
    })
    .toBe(height)
  const box = await dialog.boundingBox()
  if (!box) throw new Error('the launch form did not render')
  expect(box.y).toBeGreaterThanOrEqual(0)
  await expect(dialog.getByRole('button', { name: 'Launch' })).toBeInViewport({
    ratio: 1,
  })
})

test('a launch refusal sits below the form, not over it', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  await page.goto(alice.url)
  await page
    .getByRole('banner', { name: 'Aether' })
    .getByRole('button', { name: 'New run' })
    .tap()
  const dialog = page.getByRole('dialog', { name: 'New run' })
  await dialog.getByRole('radio', { name: /^custom/ }).tap()
  await dialog.getByLabel('Task').fill('say hello')
  await dialog.getByRole('button', { name: 'Launch', exact: true }).tap()

  const refusal = dialog.getByRole('alert').filter({ hasText: 'Launch failed' })
  await refusal.scrollIntoViewIfNeeded()
  const mode = await dialog.getByRole('radiogroup', { name: 'Mode' }).boundingBox()
  const callout = await refusal.boundingBox()
  if (!mode || !callout) throw new Error('the launch form did not render')
  expect(callout.y).toBeGreaterThanOrEqual(mode.y + mode.height)

  const width = (await dialog.boundingBox())?.width ?? 0
  const launch = await dialog.getByRole('button', { name: 'Launch', exact: true }).boundingBox()
  expect(launch?.width).toBeGreaterThan(width / 2)
})
