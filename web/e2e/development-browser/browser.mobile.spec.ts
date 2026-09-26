import { expect, test } from '../fixtures'
import { dockerReachable } from '../harness/server'
import { clickRemote, launchBrowserFixture, openBrowserPane, typeRemote } from './fixture'
import { shareWithAgent, signInAndHotUpdate } from './scenarios'

test.skip(!dockerReachable(), 'Real browser development requires a reachable Docker daemon')

test('phone operates the shared login with touch, soft keyboard and composition', async ({ page, context, aether }, testInfo) => {
  const fixture = await launchBrowserFixture(aether)
  try {
    await openBrowserPane(page, fixture)
    await signInAndHotUpdate(page, fixture, true)
    await shareWithAgent(page, fixture)
    await clickRemote(page, 80, 445, true)
    await page.getByRole('button', { name: 'Keyboard', exact: true }).click()
    await page.keyboard.press('Control+a')
    await typeRemote(page, 'Phone café ', true)
    await fixture.waitText('Note: Phone café')

    // Native Chromium IME events in the dashboard, not JS-dispatched React
    // handlers. This CDP test driver is never exposed by the product gateway.
    const driver = await context.newCDPSession(page)
    try {
      await driver.send('Input.imeSetComposition', { text: '日本語', selectionStart: 3, selectionEnd: 3 })
      await driver.send('Input.insertText', { text: '日本語' })
      await fixture.waitText('Note: Phone café 日本語')
      await expect.poll(async () => (await fixture.snapshot()).nodes.find((node) => node.role === 'textbox' && node.name === 'Shared note')?.value).toBe('Phone café 日本語')
      const contacts = await page.getByLabel('Shared browser page', { exact: true }).evaluate((node) => {
        const canvas = node as HTMLCanvasElement
        const box = canvas.getBoundingClientRect()
        const scale = Math.min(box.width / canvas.width, box.height / canvas.height)
        const left = box.left + (box.width - canvas.width * scale) / 2
        const top = box.top + (box.height - canvas.height * scale) / 2
        return [{ x: left + 60 * scale, y: top + 660 * scale, id: 1 }, { x: left + 180 * scale, y: top + 690 * scale, id: 2 }]
      })
      await driver.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: contacts })
      await fixture.waitText('Touches 2')
      await driver.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    } finally { await driver.detach() }

    await testInfo.attach('phone shared app', { body: await page.getByLabel('Shared browser page', { exact: true }).screenshot(), contentType: 'image/png' })
    const horizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)
    expect(horizontalOverflow).toBe(false)
    await clickRemote(page, 80, 365, true)
    await fixture.waitText('Signed out')
  } finally { await fixture.stop() }
})
