import type { Page } from '@playwright/test'
import { expect } from '../fixtures'
import type { DevBrowserActionResult, DevBrowserSnapshotResult, DevControlAcquireResult } from '../../src/lib/types'
import { appURL, clickRemote, typeRemote, type BrowserFixture } from './fixture'

export async function signInAndHotUpdate(page: Page, fixture: BrowserFixture, phone: boolean): Promise<void> {
  const before = await fixture.member.api.rpc<{ running: boolean }>('dev.browser.status', { run_id: fixture.runID })
  expect(before.running).toBe(false)
  await page.getByLabel('Browser URL', { exact: true }).fill(appURL)
  if (phone) await page.getByLabel('Browser viewport').selectOption('390x844')
  await page.getByRole('button', { name: 'Open browser', exact: true }).click()
  await fixture.waitText(phone ? 'Viewport 390' : 'Viewport 1280')
  await expect(page.getByText(/Live frame ·/)).toBeVisible()

  await clickRemote(page, 80, 125, phone)
  await typeRemote(page, 'test@example.invalid', phone)
  await expect.poll(async () => (await fixture.snapshot()).nodes.find((node) => node.name === 'Email')?.value).toBe('test@example.invalid')
  await clickRemote(page, 80, 200, phone)
  await typeRemote(page, 'wrong password', phone)
  await clickRemote(page, 80, 265, phone)
  await fixture.waitText('Invalid credentials')
  await clickRemote(page, 80, 200, phone)
  if (phone) await page.getByRole('button', { name: 'Keyboard', exact: true }).click()
  await page.keyboard.press('Control+a')
  await typeRemote(page, 'correct horse', phone)
  await clickRemote(page, 80, 265, phone)
  await fixture.waitText('Signed in as test@example.invalid')
  const signedIn = await fixture.currentPage()

  await fixture.hotUpdate()
  await fixture.waitText('Updated without logout')
  await fixture.waitText('Signed in as test@example.invalid')
  expect((await fixture.currentPage()).page_revision).toBe(signedIn.page_revision)
  await page.getByRole('button', { name: 'Hide browser', exact: true }).click()
  await page.getByRole('tab', { name: 'Browser', exact: true }).click()
  await expect(page.getByText(/Live frame ·/)).toBeVisible()
  await fixture.waitText('Signed in as test@example.invalid')
  await page.getByRole('button', { name: 'Reconnect', exact: true }).click()
  await expect(page.getByText(/Live frame ·/)).toBeVisible()
  const reconnected = await fixture.currentPage()
  expect(reconnected.session_id).toBe(signedIn.session_id)
  expect(reconnected.page_id).toBe(signedIn.page_id)
  await page.getByRole('button', { name: 'Reload page', exact: true }).click()
  await fixture.waitText('Signed in as test@example.invalid')
  await expect(page.getByText(/Live frame ·/)).toBeVisible()
}

export async function shareWithAgent(page: Page, fixture: BrowserFixture): Promise<void> {
  const before = await fixture.currentPage()
  await page.getByRole('button', { name: 'Release control', exact: true }).click()
  await expect(page.getByText(/Watch mode · Controller: Nobody/)).toBeVisible()
  const surface = { kind: 'browser', id: 'browser', incarnation: before.session_id }
  const acquired = await fixture.agent<DevControlAcquireResult>('control', 'acquire', { surface, control_session_id: 'real-run-agent-browser' })
  expect(acquired.controller?.kind).toBe('run_agent')
  const snapshot = await fixture.agent<DevBrowserSnapshotResult>('browser', 'snapshot', { session_id: before.session_id, page_id: before.page_id, page_revision: before.page_revision })
  const note = snapshot.nodes.find((node) => node.name === 'Shared note')
  expect(note?.node_id).toBeTruthy()
  const request = { session_id: snapshot.page.session_id, page_id: snapshot.page.page_id, page_revision: snapshot.page.page_revision, control_session_id: acquired.controller!.control_session_id, control_generation: acquired.controller!.control_generation, action: 'fill', node_id: note!.node_id, text: 'Written by the run principal' }
  await fixture.agent<DevBrowserActionResult>('browser', 'action', request)
  await fixture.waitText('Note: Written by the run principal')
  await expect(page.getByText(/Controller: Agent/)).toBeVisible()
  await page.getByRole('button', { name: 'Take over browser', exact: true }).click()
  await expect(page.getByText(/You control this browser/)).toBeVisible()
  await expect(fixture.agent('browser', 'action', { ...request, text: 'STALE AGENT MUST NOT WRITE' })).rejects.toThrow()
  await fixture.waitText('Note: Written by the run principal')
  expect((await fixture.currentPage()).page_id).toBe(before.page_id)
}
