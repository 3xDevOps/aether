import type { Locator, Page } from '@playwright/test'

import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

const viewports = [
  { viewport: { width: 1440, height: 900 }, phone: false },
  { viewport: { width: 390, height: 844 }, phone: true },
] as const

async function openLaunch(page: Page, phone: boolean): Promise<Locator> {
  const navigation = phone
    ? page.getByRole('navigation', { name: 'Main navigation' })
    : page.getByRole('navigation', { name: 'Aether' })
  await navigation.getByRole('button', { name: 'New session' }).click()
  const dialog = page.getByRole('dialog', { name: 'New session' })
  await expect(dialog).toBeVisible()
  return dialog.getByLabel('Task')
}

async function paste(page: Page, field: Locator, text: string): Promise<void> {
  await page.evaluate((value) => navigator.clipboard.writeText(value), text)
  await field.focus()
  await field.press('ControlOrMeta+V')
}

async function fieldHeight(field: Locator): Promise<number> {
  return field.evaluate((element) => element.getBoundingClientRect().height)
}

async function assertCappedAndScrollable(field: Locator): Promise<void> {
  await expect.poll(() => fieldHeight(field)).toBe(192)
  await expect.poll(() => field.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true)
}

test('Task grows on native paste and typing, caps at 192px, and can be cleared', async ({ browser, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'))

  for (const viewport of viewports) {
    const context = await browser.newContext({
      viewport: viewport.viewport,
      isMobile: viewport.phone,
      hasTouch: viewport.phone,
      colorScheme: 'dark',
    })
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: new URL(alice.url).origin })
    const page = await context.newPage()
    await page.goto(alice.url)
    await page.addStyleTag({ content: 'textarea[data-slot="textarea"] { field-sizing: fixed !important; }' })
    const field = await openLaunch(page, viewport.phone)

    const initialHeight = await fieldHeight(field)
    const multiline = Array.from({ length: 24 }, (_, index) => `line ${index + 1}`).join('\n')
    await paste(page, field, multiline)
    await expect(field).toHaveValue(multiline)
    await assertCappedAndScrollable(field)
    await field.evaluate((element) => { element.scrollTop = 0 })
    await field.press('End')
    await expect.poll(() => field.evaluate((element) => element.scrollTop)).toBeGreaterThan(0)

    await field.fill('')
    await field.pressSequentially('typed first line\ntyped second line\ntyped third line')
    await expect(field).toHaveValue('typed first line\ntyped second line\ntyped third line')
    await expect.poll(() => fieldHeight(field)).toBeGreaterThan(initialHeight)

    await field.fill('')
    const paragraph = 'long prompt word '.repeat(160)
    await paste(page, field, paragraph)
    await expect(field).toHaveValue(paragraph)
    await assertCappedAndScrollable(field)
    await field.press('End')
    await field.pressSequentially('x')
    await expect(field).toHaveValue(paragraph + 'x')
    await expect.poll(() => field.evaluate((element) => element.scrollTop)).toBeGreaterThan(0)

    await field.fill('prefilled prompt')
    await field.press('End')
    const appended = '\n' + 'padded prompt word '.repeat(100)
    await paste(page, field, appended)
    await expect(field).toHaveValue('prefilled prompt' + appended)
    await assertCappedAndScrollable(field)

    await field.press('ControlOrMeta+A')
    await field.press('Backspace')
    await expect(field).toHaveValue('')
    await expect.poll(() => fieldHeight(field)).toBe(initialHeight)

    const sharedURL = new URL(alice.url)
    sharedURL.searchParams.set('new', '1')
    sharedURL.searchParams.set('prompt', multiline)
    await page.goto(sharedURL.toString())
    await page.addStyleTag({ content: 'textarea[data-slot="textarea"] { field-sizing: fixed !important; }' })
    const sharedField = page.getByRole('dialog', { name: 'New session' }).getByLabel('Task')
    await expect(sharedField).toHaveValue(multiline)
    await expect.poll(() => fieldHeight(sharedField)).toBeGreaterThan(192)
    await expect.poll(() => sharedField.evaluate((element) => element.scrollHeight - element.clientHeight)).toBeLessThanOrEqual(1)
    await sharedField.press('End')
    await sharedField.pressSequentially('x')
    await expect(sharedField).toHaveValue(multiline + 'x')
    await assertCappedAndScrollable(sharedField)

    await context.close()
  }
})
