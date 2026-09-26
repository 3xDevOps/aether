import { expect, test } from '../fixtures'
import { dockerReachable } from '../harness/server'
import type { DevBrowserPagesResult, DevControlStatusResult } from '../../src/lib/types'
import { clickRemote, launchBrowserFixture, openBrowserPane } from './fixture'
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
    await expect(page.getByLabel('Browser page', { exact: true })).toContainText('Authentication popup')
    await page.getByLabel('Browser page', { exact: true }).selectOption(popup.page_id)
    await fixture.waitText('Shared popup')
    page.once('dialog', (dialog) => void dialog.accept())
    await page.getByRole('button', { name: 'Close page', exact: true }).click()
    await fixture.waitText('Signed in as test@example.invalid')
    expect((await fixture.currentPage()).page_id).toBe(beforePopup.page_id)

    const stalePage = await fixture.currentPage()
    const ownership = await fixture.member.api.rpc<DevControlStatusResult>('dev.control.status', { run_id: fixture.runID, surface: { kind: 'browser', id: 'browser', incarnation: stalePage.session_id } })
    await page.getByLabel('Browser viewport').selectOption('390x844')
    await fixture.waitText('Viewport 390')
    await expect(fixture.member.api.rpc('dev.browser.action', { run_id: fixture.runID, session_id: stalePage.session_id, page_id: stalePage.page_id, page_revision: stalePage.page_revision, viewport_id: stalePage.viewport_id, control_session_id: ownership.controller!.control_session_id, control_generation: ownership.controller!.control_generation, action: 'pointer', phase: 'click', x: 80, y: 365 })).rejects.toThrow(/viewport|stale/i)
    await fixture.waitText('Signed in as test@example.invalid')

    const watcher = await browser.newContext()
    try {
      const watchPage = await watcher.newPage()
      await watchPage.goto(`${fixture.member.url}&run=${fixture.runID}`)
      await watchPage.getByRole('tab', { name: 'Browser', exact: true }).click()
      await expect(watchPage.getByText(/Watch mode · Controller: Member/)).toBeVisible()
      await expect(watchPage.getByText(/Live frame ·/)).toBeVisible()
      // Clicking the actual rendered logout while watching must not mutate it.
      await clickRemote(watchPage, 80, 365)
      await fixture.waitText('Signed in as test@example.invalid')
      await watchPage.getByRole('button', { name: 'Take over browser', exact: true }).click()
      await expect(watchPage.getByText(/You control this browser/)).toBeVisible()
      await expect(page.getByText(/Watch mode · Controller: Member/)).toBeVisible()
      await expect(fixture.member.api.rpc('dev.browser.action', { run_id: fixture.runID, session_id: stalePage.session_id, page_id: stalePage.page_id, page_revision: stalePage.page_revision, control_session_id: ownership.controller!.control_session_id, control_generation: ownership.controller!.control_generation, action: 'key', key: 'Enter' })).rejects.toThrow(/stale|control|lease/i)
      await clickRemote(watchPage, 80, 365)
      await fixture.waitText('Signed out')
      await watchPage.getByRole('button', { name: 'Screenshot', exact: true }).click()
      await expect(watchPage.getByText(/nothing has been published/)).toBeVisible()
      watchPage.once('dialog', (dialog) => void dialog.accept())
      await watchPage.getByRole('button', { name: 'Reset session', exact: true }).click()
      await expect.poll(async () => (await fixture.member.api.rpc<{ session_id: string }>('dev.browser.status', { run_id: fixture.runID })).session_id).not.toBe(stalePage.session_id)
      await expect(watchPage.getByRole('button', { name: 'Open browser', exact: true })).toBeDisabled()
      await watchPage.getByRole('button', { name: 'Acquire control', exact: true }).click()
      await watchPage.getByRole('button', { name: 'Open browser', exact: true }).click()
      await fixture.waitText('Signed out')
    } finally { await watcher.close() }
  } finally { await fixture.stop() }
})
