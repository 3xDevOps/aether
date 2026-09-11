// Dialogs on a phone. Below `sm` they anchor to the top instead of centring,
// so a soft keyboard cannot take the footer with it: iOS Safari ignores
// `interactive-widget=resizes-content`, and a centred fixed dialog there ends
// up behind the keyboard while a top-anchored one stays in the visual
// viewport.

import { seedWorkspace } from './harness/setup'
import { expect, shrinkToKeyboardHeight, test } from './mobile'

/** The 1rem inset `top-4` puts a narrow-viewport dialog at. */
const inset = 16

test('a confirm sits at the top of a phone screen, not its middle', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  // A short dialog is the only kind that can tell the two apart: anything
  // taller than the screen is clamped by `max-h-[calc(100dvh-2rem)]` and
  // lands at the same 1rem either way.
  await alice.api.rpc('template.save', {
    workspace_id: workspaces[0].id,
    name: 'nightly',
    task: 'run the nightly sweep',
    harness: 'fake',
  })

  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  await page
    .getByRole('dialog', { name: 'Runs' })
    .getByRole('button', { name: 'Templates', exact: true })
    .tap()
  await page.getByRole('button', { name: 'Delete', exact: true }).tap()

  const confirm = page.getByRole('alertdialog')
  await expect(confirm).toBeVisible()
  // Polled: the dialog settles into place through its open animation.
  await expect
    .poll(async () => (await confirm.boundingBox())?.y ?? -1)
    .toBeLessThanOrEqual(inset)

  const box = await confirm.boundingBox()
  const viewport = await page.evaluate(() => window.innerHeight)
  if (!box) throw new Error('the confirm did not render')
  // Centred, this dialog would start halfway down the screen. Without the gap
  // the assertion above would pass on a centred dialog too.
  expect((viewport - box.height) / 2).toBeGreaterThan(inset + 100)
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

  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  await page
    .getByRole('dialog', { name: 'Runs' })
    .getByRole('button', { name: 'New run' })
    .tap()

  const dialog = page.getByRole('dialog', { name: 'Launch a run' })
  await expect(dialog).toBeVisible()

  await expect
    .poll(async () => (await dialog.boundingBox())?.y ?? -1)
    .toBeLessThanOrEqual(inset)
  const box = await dialog.boundingBox()
  const height = await page.evaluate(() => window.innerHeight)
  if (!box) throw new Error('the launch form did not render')
  // The form is taller than this viewport, so what keeps the footer reachable
  // is the dialog being clamped to the viewport and scrolling inside itself.
  expect(box.y + box.height).toBeLessThanOrEqual(height)
  await expect(dialog.getByRole('button', { name: 'Launch' })).toBeInViewport({
    ratio: 1,
  })
})
