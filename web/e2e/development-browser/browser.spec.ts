import { expect, test } from '../fixtures'
import { dockerReachable } from '../harness/server'
import type { DevBrowserPagesResult, DevControlStatusResult } from '../../src/lib/types'
import { addressBar, appAddress, browserAction, chooseViewport, clickRemote, expectLive, launchBrowserFixture, openBrowserPane, remotePage } from './fixture'
import { shareWithAgent, signInAndHotUpdate } from './scenarios'

test.skip(!dockerReachable(), 'Real browser development requires a reachable Docker daemon')

test('real shared login, HMR, popups, watcher takeover and stale authority', async ({ page, browser, aether }, testInfo) => {
  const fixture = await launchBrowserFixture(aether)
  const driving = page.getByRole('img', { name: 'You are driving', exact: true })
  try {
    await openBrowserPane(page, fixture)
    await signInAndHotUpdate(page, fixture, false)
    await shareWithAgent(page, fixture, false)
    await clickRemote(page, 80, 660, false, 2)
    await fixture.waitText('Double click received')
    await page.mouse.wheel(0, 600)
    await fixture.waitText('Scroll 600')
    await page.mouse.wheel(0, -600)
    await fixture.waitText('Scroll 0')
    await testInfo.attach('shared authenticated app', { body: await remotePage(page).screenshot(), contentType: 'image/png' })

    // Beside the run's first view and back: the same page, and no button to drive it again.
    await page.getByRole('button', { name: 'Show beside Terminal', exact: true }).click()
    const beside = page.getByRole('region', { name: 'Browser', exact: true })
    await expect(beside.getByRole('toolbar', { name: 'Browser', exact: true })).toBeVisible()
    await expect(page.getByRole('tab', { name: 'Browser', exact: true })).toHaveCount(0)
    await expect(page.getByRole('tab', { name: 'Terminal', exact: true })).toHaveAttribute('aria-selected', 'true')
    await expectLive(page)
    await page.getByRole('button', { name: 'Show the Browser as a tab', exact: true }).click()
    await expect(page.getByRole('tab', { name: 'Browser', exact: true })).toHaveAttribute('aria-selected', 'true')
    await expectLive(page)
    await fixture.waitText('Signed in as test@example.invalid')

    const beforePopup = await fixture.currentPage()
    const pages = page.getByRole('tablist', { name: 'Pages', exact: true })
    await expect(pages).toHaveCount(0)
    await clickRemote(page, 70, 540)
    await expect.poll(async () => (await fixture.member.api.rpc<DevBrowserPagesResult>('dev.browser.pages', { run_id: fixture.runID, session_id: beforePopup.session_id })).pages.some((item) => item.title === 'Authentication popup')).toBe(true)
    const inventory = await fixture.member.api.rpc<DevBrowserPagesResult>('dev.browser.pages', { run_id: fixture.runID, session_id: beforePopup.session_id })
    const popup = inventory.pages.find((item) => item.title === 'Authentication popup')!
    // A page that opened by itself is marked until it is selected.
    await pages.getByRole('tab', { name: 'Authentication popup (new)', exact: true }).click()
    await fixture.waitText('Shared popup')
    await expect(pages.getByRole('tab', { name: 'Authentication popup', exact: true })).toHaveAttribute('aria-selected', 'true')
    const actions = page.getByRole('button', { name: 'Browser actions', exact: true })
    await actions.focus()
    await page.keyboard.press('Enter')
    await page.getByRole('menuitem', { name: 'Close page…', exact: true }).click()
    const closeDialog = page.getByRole('alertdialog')
    await expect(closeDialog.getByRole('button', { name: 'Cancel', exact: true })).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(actions).toBeFocused()
    expect((await fixture.currentPage()).page_id).toBe(popup.page_id)
    await actions.press('Enter')
    await page.getByRole('menuitem', { name: 'Close page…', exact: true }).click()
    await closeDialog.getByRole('button', { name: 'Close page', exact: true }).click()
    await fixture.waitText('Signed in as test@example.invalid')
    expect((await fixture.currentPage()).page_id).toBe(beforePopup.page_id)
    // The lease belongs to the browser, not to the page that closed.
    await expect(pages).toHaveCount(0)
    await expect(driving).toBeVisible()
    await expectLive(page)

    const stalePage = await fixture.currentPage()
    const ownership = await fixture.member.api.rpc<DevControlStatusResult>('dev.control.status', { run_id: fixture.runID, surface: { kind: 'browser', id: 'browser', incarnation: stalePage.session_id } })
    await chooseViewport(page, 'Phone', 390)
    await fixture.waitText('Viewport 390')
    await expect(fixture.member.api.rpc('dev.browser.action', { run_id: fixture.runID, session_id: stalePage.session_id, page_id: stalePage.page_id, page_revision: stalePage.page_revision, viewport_id: stalePage.viewport_id, control_session_id: ownership.controller!.control_session_id, control_generation: ownership.controller!.control_generation, action: 'pointer', phase: 'click', x: 80, y: 365 })).rejects.toThrow(/viewport|stale/i)
    await fixture.waitText('Signed in as test@example.invalid')

    const watcher = await browser.newContext()
    try {
      const watchPage = await watcher.newPage()
      const watcherDriving = watchPage.getByRole('img', { name: 'You are driving', exact: true })
      await watchPage.goto(`${fixture.member.url}&run=${fixture.runID}&view=browser`)
      // The same member in a second dashboard has its own control identity.
      await expect(watchPage.getByText('You are driving in another tab', { exact: true })).toBeVisible()
      await expectLive(watchPage)
      // Clicking the actual rendered logout while watching must not mutate it.
      await clickRemote(watchPage, 80, 365)
      await expect(watchPage.getByText('You are driving in another tab. Take over to use the page.', { exact: true })).toBeVisible()
      await fixture.waitText('Signed in as test@example.invalid')
      await watchPage.getByRole('button', { name: 'Take over', exact: true }).click()
      await expect(watcherDriving).toBeVisible()
      await expect(page.getByText('You are driving in another tab', { exact: true })).toBeVisible()
      await expect(driving).toHaveCount(0)
      await expect(fixture.member.api.rpc('dev.browser.action', { run_id: fixture.runID, session_id: stalePage.session_id, page_id: stalePage.page_id, page_revision: stalePage.page_revision, control_session_id: ownership.controller!.control_session_id, control_generation: ownership.controller!.control_generation, action: 'key', key: 'Enter' })).rejects.toThrow(/stale|control|lease/i)
      // The watcher's own viewport choice applies now that it drives.
      await chooseViewport(watchPage, 'Desktop', 1280)
      await fixture.waitText('Viewport 1280')
      await clickRemote(watchPage, 80, 365)
      await fixture.waitText('Signed out')
      await browserAction(watchPage, 'Screenshot')
      await expect(watchPage.getByText(/nothing has been published/)).toBeVisible()
      await browserAction(watchPage, 'Reset session…')
      await watchPage.getByRole('alertdialog').getByRole('button', { name: 'Reset session', exact: true }).click()
      await expect.poll(async () => (await fixture.member.api.rpc<{ session_id: string }>('dev.browser.status', { run_id: fixture.runID })).session_id).not.toBe(stalePage.session_id)
      // The new session is free: the address bar opens its first page with no button in between.
      await expect(watchPage.getByRole('heading', { name: 'Open a page', exact: true })).toBeVisible()
      await addressBar(watchPage).fill(appAddress)
      await addressBar(watchPage).press('Enter')
      await fixture.waitText('Signed out')
      await expect(watcherDriving).toBeVisible()
    } finally { await watcher.close() }
  } finally { await fixture.stop() }
})
