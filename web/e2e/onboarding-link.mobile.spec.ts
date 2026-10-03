// Linking from a phone on what a soft keyboard leaves of the screen.
//
// A keyboard takes most of a phone's screen, and what is left has to hold
// the field being typed into and the button that submits it.
// `shrinkToKeyboardHeight` takes the viewport down to what is left, which is
// what a keyboard does to the layout viewport wherever the shell's
// `interactive-widget=resizes-content` is honoured - see e2e/mobile.ts.

import { expect, shrinkToKeyboardHeight, test } from './mobile'
import { OnboardingWizard } from './pages/wizard'

test('the Link step stays usable at the height a keyboard leaves', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const wizard = await OnboardingWizard.open(page, alice.url)

  await wizard.link.byAddress().tap()
  const address = wizard.link.section.getByLabel('Server address')
  await address.tap()
  const restoreViewport = await shrinkToKeyboardHeight(page)

  await expect(address).toBeFocused()
  await expect(address).toBeInViewport({ ratio: 1 })
  await page.keyboard.type(aether.server.addr)
  await expect(address).toHaveValue(aether.server.addr)

  // The shell owns the screen, so the short viewport must not turn the page
  // itself into a scroller: the step scrolls inside its own pane.
  const grew = await page.evaluate(
    () =>
      document.documentElement.scrollHeight -
      document.documentElement.clientHeight,
  )
  expect(grew).toBe(0)

  // The submit is the other half. The wizard's header and step list fill a
  // phone screen on their own, so it starts below the fold: what the short
  // viewport may not do is keep it from being scrolled into reach.
  const link = wizard.link.button('Link')
  await link.scrollIntoViewIfNeeded()
  await expect(link).toBeInViewport({ ratio: 1 })
  await link.tap()
  await expect(wizard.link.section).toContainText('(admin)')

  await restoreViewport()
  await wizard.link.continue().tap()
  await wizard.expectStep('Git identity')
})

test('repository settings keep their content inside the phone after a Git push', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await alice.api.rpc('workspace.add', {
    name: 'project',
    base_branch: 'main',
    environment: {},
  })
  await page.setViewportSize({ width: 390, height: 600 })
  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Commands', exact: true }).tap()
  await page.getByPlaceholder('Search commands, runs, workspaces...').fill('Manage workspaces')
  await page.getByRole('option', { name: 'Manage workspaces' }).tap()
  await page.getByRole('button', { name: 'Link local repository', exact: true }).tap()

  const dialog = page.getByRole('dialog', { name: 'Workspace repository', exact: true })
  await dialog.getByLabel('Repository path').fill(repo)
  await dialog.getByRole('button', { name: 'Add remote', exact: true }).tap()
  await dialog.getByRole('button', { name: 'Push now', exact: true }).tap()
  await expect(dialog).toContainText('Pushed main to aether')

  await expect
    .poll(() => dialog.evaluate((element) => element.scrollWidth - element.clientWidth))
    .toBe(0)
  const bounds = await dialog.evaluate((element) => {
    const { left, right } = element.getBoundingClientRect()
    return { left, right, viewport: window.innerWidth }
  })
  expect(bounds.left).toBeGreaterThanOrEqual(0)
  expect(bounds.right).toBeLessThanOrEqual(bounds.viewport)

  const repository = dialog.getByRole('region', { name: 'Workspace repository', exact: true })
  for (const text of [
    dialog.getByRole('heading', { name: 'Workspace repository', exact: true }),
    dialog.locator('[data-slot="dialog-description"]'),
    repository.locator('p').first(),
  ]) {
    await text.scrollIntoViewIfNeeded()
    await expect(text).toBeInViewport({ ratio: 1 })
    const fits = await text.evaluate((element) => {
      const range = document.createRange()
      range.selectNodeContents(element)
      const { left, right } = element.closest('[role="dialog"]')!.getBoundingClientRect()
      return Array.from(range.getClientRects()).every(
        (rect) => rect.left >= left && rect.right <= right,
      )
    })
    expect(fits).toBe(true)
  }
})
