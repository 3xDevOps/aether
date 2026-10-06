import type { Locator } from '@playwright/test'
import { expect, test } from './fixtures'
import type { WorkspaceResult } from './harness/client'
import { seedWorkspace } from './harness/setup'

// Workspace setup needs the real gateway/server, but does not launch a Docker run.
// A short touch viewport exercises the group heading plus coarse option heights.
test.use({ viewport: { width: 640, height: 420 }, hasTouch: true })

async function expectActiveVisible(input: Locator, list: Locator, option: Locator) {
  await expect(option).toHaveAttribute('aria-selected', 'true')
  await expect(input).toHaveAttribute('aria-activedescendant', (await option.getAttribute('id'))!)
  await expect(input).toBeFocused()
  await expect.poll(async () => {
    const row = await option.boundingBox()
    const viewport = await list.boundingBox()
    if (!row || !viewport) return false
    return row.y >= viewport.y - 1 && row.y + row.height <= viewport.y + viewport.height + 1
  }).toBe(true)
}

test('palette announces initial selection, scrolls keyboard results, restores browse order and navigates', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const workspaceIDs: string[] = []
  for (let index = 0; index < 24; index++) {
    const { workspace } = await alice.api.rpc<WorkspaceResult>('workspace.add', {
      name: `Palette workspace ${String(index).padStart(2, '0')}`,
      base_branch: 'main',
      environment: {},
    })
    workspaceIDs.push(workspace.id)
  }
  await page.goto(alice.url)
  const trigger = page.getByRole('button', { name: 'Search', exact: true })
  await trigger.click()
  const dialog = page.getByRole('dialog', { name: 'Command Palette' })
  const input = dialog.getByRole('combobox')
  const list = dialog.getByRole('listbox')
  const options = list.getByRole('option')
  const enabled = list.locator('[role="option"]:not([aria-disabled="true"])')
  await expectActiveVisible(input, list, enabled.first())
  const browse = await options.allTextContents()

  await input.press('End')
  await expectActiveVisible(input, list, enabled.last())
  await input.press('ArrowUp')
  await expectActiveVisible(input, list, enabled.nth((await enabled.count()) - 2))
  await input.press('Home')
  await expectActiveVisible(input, list, enabled.first())

  await input.fill('Palette workspace 23')
  const destination = list.getByRole('option').filter({ hasText: 'Palette workspace 23' })
  await expectActiveVisible(input, list, destination)
  await input.fill('no-matching-command-zzzzzz')
  await expect(options).toHaveCount(0)
  await expect(input).not.toHaveAttribute('aria-activedescendant')
  await input.fill('')
  await expect(options).toHaveText(browse)
  await expectActiveVisible(input, list, enabled.first())

  // workspace.add has no catalog event; workspace.deleted updates the live catalog.
  await input.fill('Palette workspace 23')
  await expectActiveVisible(input, list, destination)
  const removedText = (await destination.textContent())!
  const filtered = await options.allTextContents()
  const remainingResults = filtered.filter((text) => text !== removedText)
  await alice.api.rpc('workspace.delete', { workspace_id: workspaceIDs[23] })
  await expect(destination).toHaveCount(0)
  await expect(input).toHaveValue('Palette workspace 23')
  await expect(options).toHaveText(remainingResults)
  if (remainingResults.length > 0) {
    await expectActiveVisible(input, list, enabled.first())
  } else {
    await expect(input).not.toHaveAttribute('aria-activedescendant')
    await expect(input).toBeFocused()
  }
  await input.fill('')
  await expect(options).toHaveText(browse.filter((text) => text !== removedText))
  await expectActiveVisible(input, list, enabled.first())

  await input.fill('Palette workspace 22')
  const remaining = list.getByRole('option').filter({ hasText: 'Palette workspace 22' })
  await expectActiveVisible(input, list, remaining)
  await input.press('Enter')
  await expect(dialog).toHaveCount(0)
  // Under 768px the top bar carries the view title.
  await expect(page.getByRole('banner')).toContainText('Palette workspace 22')

  await trigger.click()
  await expectActiveVisible(input, list, enabled.first())
  await input.press('Tab')
  await expect.poll(() => dialog.evaluate((node) => node.contains(document.activeElement))).toBe(true)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await expect(trigger).toBeFocused()
})
