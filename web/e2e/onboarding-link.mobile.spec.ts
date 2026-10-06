// Linking from a phone on what a soft keyboard leaves of the screen.
//
// A keyboard takes most of a phone's screen, and what is left has to hold
// the field being typed into and the button that submits it.
// `shrinkToKeyboardHeight` takes the viewport down to what is left, which is
// what a keyboard does to the layout viewport wherever the shell's
// `interactive-widget=resizes-content` is honoured - see e2e/mobile.ts.

import { expect, shrinkToKeyboardHeight, test } from './mobile'
import { OnboardingWizard } from './pages/wizard'

test('the Connect step stays usable at the height a keyboard leaves', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const wizard = await OnboardingWizard.open(page, alice.url)

  await wizard.connect.byAddress().tap()
  const address = wizard.connect.section.getByLabel('Server address')
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
  const link = wizard.connect.button('Link')
  await link.scrollIntoViewIfNeeded()
  await expect(link).toBeInViewport({ ratio: 1 })
  await link.tap()
  await expect(wizard.connect.section).toContainText('(admin)')

  await restoreViewport()
  const next = wizard.connect.continue()
  await next.scrollIntoViewIfNeeded()
  await next.tap()
  await wizard.expectStep('Repository')
})

test('the repository page keeps its content inside the phone after a Git push', async ({
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
  await page.getByRole('button', { name: 'Search', exact: true }).tap()
  await page.getByPlaceholder('Search commands, runs, workspaces…').fill('Manage workspaces')
  await page.getByRole('option', { name: 'Manage workspaces' }).tap()
  await page.getByRole('button', { name: 'More actions for project' }).tap()
  await page.getByRole('menuitem', { name: 'Repository' }).tap()
  await page.getByRole('button', { name: 'Link local repository', exact: true }).tap()

  const main = page.getByRole('main')
  const repository = main.getByRole('region', { name: 'Workspace repository', exact: true })
  await repository.getByLabel('Repository path').fill(repo)
  await repository.getByRole('button', { name: 'Add remote', exact: true }).tap()
  await repository.getByRole('button', { name: 'Push now', exact: true }).tap()
  await expect(repository).toContainText('Pushed main to aether')

  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBe(0)
  for (const text of [
    main.getByRole('heading', { name: 'Local clone', exact: true }),
    repository.locator('p').first(),
  ]) {
    await text.scrollIntoViewIfNeeded()
    await expect(text).toBeInViewport({ ratio: 1 })
    const fits = await text.evaluate((element) => {
      const range = document.createRange()
      range.selectNodeContents(element)
      return Array.from(range.getClientRects()).every((rect) => rect.left >= 0 && rect.right <= window.innerWidth)
    })
    expect(fits).toBe(true)
  }
})
