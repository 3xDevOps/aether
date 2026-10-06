import { expect, test } from '../fixtures'
import { dockerReachable } from '../harness/server'
import type { DevBrowserPagesResult, DevControlStatusResult } from '../../src/lib/types'
import { appURL, clickRemote, launchBrowserFixture, openBrowserPane } from './fixture'
import { shareWithAgent, signInAndHotUpdate } from './scenarios'

test.skip(!dockerReachable(), 'Real browser development requires a reachable Docker daemon')

test('real shared login, HMR, popups, watcher takeover and stale authority', async ({ page, browser, aether }, testInfo) => {
  const fixture = await launchBrowserFixture(aether)
  try {
    await openBrowserPane(page, fixture)
    await signInAndHotUpdate(page, fixture, false)
    await shareWithAgent(page, fixture)
    await clickRemote(page, 80, 660, false, 2)
    await fixture.waitText('Double click received')
    await page.mouse.wheel(0, 600)
    await fixture.waitText('Scroll 600')
    await page.mouse.wheel(0, -600)
    await fixture.waitText('Scroll 0')
    await testInfo.attach('shared authenticated app', { body: await page.getByLabel('Shared browser page', { exact: true }).screenshot(), contentType: 'image/png' })

    const beforePopup = await fixture.currentPage()
    await clickRemote(page, 70, 540)
    await expect.poll(async () => (await fixture.member.api.rpc<DevBrowserPagesResult>('dev.browser.pages', { run_id: fixture.runID, session_id: beforePopup.session_id })).pages.some((item) => item.title === 'Authentication popup')).toBe(true)
    const inventory = await fixture.member.api.rpc<DevBrowserPagesResult>('dev.browser.pages', { run_id: fixture.runID, session_id: beforePopup.session_id })
    const popup = inventory.pages.find((item) => item.title === 'Authentication popup')!
    await page.getByRole('button', { name: 'Page tools', exact: true }).click()
    await expect(page.getByLabel('Browser page', { exact: true })).toContainText('Authentication popup')
    await page.getByLabel('Browser page', { exact: true }).selectOption(popup.page_id)
    await fixture.waitText('Shared popup')
    await page.keyboard.press('Escape')
    const tools = page.getByRole('button', { name: 'Page tools', exact: true })
    await tools.focus()
    await page.keyboard.press('Enter')
    await page.getByRole('button', { name: 'Close page', exact: true }).click()
    const closeDialog = page.getByRole('alertdialog')
    await expect(closeDialog.getByRole('button', { name: 'Cancel', exact: true })).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(tools).toBeFocused()
    expect((await fixture.currentPage()).page_id).toBe(popup.page_id)
    await tools.press('Enter')
    await page.getByRole('button', { name: 'Close page', exact: true }).click()
    await closeDialog.getByRole('button', { name: 'Close page', exact: true }).click()
    await fixture.waitText('Signed in as test@example.invalid')
    expect((await fixture.currentPage()).page_id).toBe(beforePopup.page_id)
    // Closing the streamed page disconnects its controller; the surviving
    // page is observed first and requires an explicit new acquisition.
    await expect(page.getByText(/Nobody is driving/)).toBeVisible()
    await page.getByRole('button', { name: 'Take control', exact: true }).click()
    await expect(page.getByText(/You are driving/)).toBeVisible()

    const stalePage = await fixture.currentPage()
    const ownership = await fixture.member.api.rpc<DevControlStatusResult>('dev.control.status', { run_id: fixture.runID, surface: { kind: 'browser', id: 'browser', incarnation: stalePage.session_id } })
    await page.getByRole('button', { name: 'Page tools', exact: true }).click()
    await page.getByLabel('Browser viewport').selectOption('390x844')
    await fixture.waitText('Viewport 390')
    await page.keyboard.press('Escape')
    await expect(fixture.member.api.rpc('dev.browser.action', { run_id: fixture.runID, session_id: stalePage.session_id, page_id: stalePage.page_id, page_revision: stalePage.page_revision, viewport_id: stalePage.viewport_id, control_session_id: ownership.controller!.control_session_id, control_generation: ownership.controller!.control_generation, action: 'pointer', phase: 'click', x: 80, y: 365 })).rejects.toThrow(/viewport|stale/i)
    await fixture.waitText('Signed in as test@example.invalid')

    const watcher = await browser.newContext()
    try {
      const watchPage = await watcher.newPage()
      await watchPage.goto(`${fixture.member.url}&run=${fixture.runID}&view=browser`)
      await expect(watchPage.getByText(/^Browser running · (?!Nobody|You|The agent).+ is driving$/)).toBeVisible()
      await expect(watchPage.getByText(/Live frame ·/)).toBeVisible()
      await watchPage.getByRole('button', { name: 'Page tools', exact: true }).click()
      await expect(watchPage.getByRole('button', { name: 'Close page', exact: true })).toBeDisabled()
      await expect(watchPage.getByRole('button', { name: 'Reset session', exact: true })).toBeDisabled()
      await expect(watchPage.getByRole('button', { name: 'Screenshot', exact: true })).toBeEnabled()
      await watchPage.keyboard.press('Escape')
      // Clicking the actual rendered logout while watching must not mutate it.
      await clickRemote(watchPage, 80, 365)
      await fixture.waitText('Signed in as test@example.invalid')
      await watchPage.getByRole('button', { name: 'Take over', exact: true }).click()
      await expect(watchPage.getByText(/You are driving/)).toBeVisible()
      await expect(page.getByText(/^Browser running · (?!Nobody|You|The agent).+ is driving$/)).toBeVisible()
      await expect(fixture.member.api.rpc('dev.browser.action', { run_id: fixture.runID, session_id: stalePage.session_id, page_id: stalePage.page_id, page_revision: stalePage.page_revision, control_session_id: ownership.controller!.control_session_id, control_generation: ownership.controller!.control_generation, action: 'key', key: 'Enter' })).rejects.toThrow(/stale|control|lease/i)
      await clickRemote(watchPage, 80, 365)
      await fixture.waitText('Signed out')
      await watchPage.getByRole('button', { name: 'Page tools', exact: true }).click()
      await watchPage.getByRole('button', { name: 'Screenshot', exact: true }).click()
      await expect(watchPage.getByText(/nothing has been published/)).toBeVisible()
      await watchPage.getByRole('button', { name: 'Reset session', exact: true }).click()
      await watchPage.getByRole('alertdialog').getByRole('button', { name: 'Reset session', exact: true }).click()
      await expect.poll(async () => (await fixture.member.api.rpc<{ session_id: string }>('dev.browser.status', { run_id: fixture.runID })).session_id).not.toBe(stalePage.session_id)
      // The backend reset can finish before the pane observes its new identity.
      // Take control only after the rendered surface has dropped the old fence.
      await expect(watchPage.getByRole('heading', { name: 'No page open' })).toBeVisible()
      await watchPage.getByRole('button', { name: 'Take control', exact: true }).click()
      await watchPage.getByRole('button', { name: 'Other address…', exact: true }).click()
      await watchPage.getByLabel('Browser URL', { exact: true }).fill(appURL)
      await watchPage.getByRole('button', { name: 'Open', exact: true }).click()
      await fixture.waitText('Signed out')
    } finally { await watcher.close() }
  } finally { await fixture.stop() }
})
