import type { Page } from '@playwright/test'
import { expect } from '../fixtures'
import type { DevBrowserActionResult, DevBrowserSnapshotResult, DevControlAcquireResult } from '../../src/lib/types'
import { addressBar, appAddress, browserAction, chooseViewport, clickRemote, expectLive, remotePage, typeRemote, type BrowserFixture } from './fixture'

const driving = (page: Page) => page.getByRole('img', { name: 'You are driving', exact: true })

export async function signInAndHotUpdate(page: Page, fixture: BrowserFixture, phone: boolean): Promise<void> {
  const before = await fixture.member.api.rpc<{ running: boolean }>('dev.browser.status', { run_id: fixture.runID })
  expect(before.running).toBe(false)
  await expect(page.getByRole('button', { name: /Take control|Take over/ })).toHaveCount(0)
  await addressBar(page).fill(appAddress)
  await addressBar(page).press('Enter')
  await expect(page.getByText('Starting the browser', { exact: true })).toBeVisible()
  // The first page has the browser's start-up budget, not the default expectation's.
  await fixture.waitText('Viewport')
  await expectLive(page)
  await expect(driving(page)).toBeVisible()
  await expect(addressBar(page)).toHaveValue(`http://${appAddress}/`)

  // The first page opens at the pane's size, so nothing is letterboxed.
  const fitted = await remotePage(page).evaluate((element) => {
    const pane = element.parentElement!.getBoundingClientRect()
    return { frame: [(element as HTMLCanvasElement).width, (element as HTMLCanvasElement).height], pane: [Math.floor(pane.width), Math.floor(pane.height)] }
  })
  expect(fitted.frame).toEqual(fitted.pane)
  await fixture.waitText(`Viewport ${fitted.pane[0]}`)

  // The fixture app is laid out for these sizes; the rest of the scenario uses a preset.
  await chooseViewport(page, phone ? 'Phone' : 'Desktop', phone ? 390 : 1280)
  await fixture.waitText(phone ? 'Viewport 390' : 'Viewport 1280')

  await clickRemote(page, 80, 125, phone)
  await typeRemote(page, 'test@example.invalid', phone)
  await expect.poll(async () => (await fixture.snapshot()).nodes.find((node) => node.role === 'textbox' && node.name === 'Email')?.value).toBe('test@example.invalid')
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

  // Leaving the view gives the lease up; coming back needs no button to take it again.
  await page.getByRole('tab', { name: 'Terminal', exact: true }).click()
  await page.getByRole('tab', { name: 'Browser', exact: true }).click()
  await expectLive(page)
  await fixture.waitText('Signed in as test@example.invalid')
  const returned = await fixture.currentPage()
  expect(returned.session_id).toBe(signedIn.session_id)
  expect(returned.page_id).toBe(signedIn.page_id)
  await expect(driving(page)).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Take control|Take over/ })).toHaveCount(0)
  await page.getByRole('button', { name: 'Reload', exact: true }).click()
  await expect(driving(page)).toBeVisible()
  await fixture.waitText('Signed in as test@example.invalid')
  await expectLive(page)
  await expect(remotePage(page)).toHaveAttribute('width', phone ? '390' : '1280')
}

export async function shareWithAgent(page: Page, fixture: BrowserFixture, phone: boolean): Promise<void> {
  const before = await fixture.currentPage()
  await browserAction(page, 'Release control')
  await expect(driving(page)).toHaveCount(0)
  const surface = { kind: 'browser', id: 'browser', incarnation: before.session_id }
  const acquired = await fixture.agent<DevControlAcquireResult>('control', 'acquire', { surface, control_session_id: 'real-run-agent-browser' })
  expect(acquired.controller?.kind).toBe('run_agent')
  const snapshot = await fixture.agent<DevBrowserSnapshotResult>('browser', 'snapshot', { session_id: before.session_id, page_id: before.page_id, page_revision: before.page_revision })
  const note = snapshot.nodes.find((node) => node.role === 'textbox' && node.name === 'Shared note')
  expect(note?.node_id).toBeTruthy()
  const request = { session_id: snapshot.page.session_id, page_id: snapshot.page.page_id, page_revision: snapshot.page.page_revision, control_session_id: acquired.controller!.control_session_id, control_generation: acquired.controller!.control_generation, action: 'fill', node_id: note!.node_id, text: 'Written by the run principal' }
  await fixture.agent<DevBrowserActionResult>('browser', 'action', request)
  await fixture.waitText('Note: Written by the run principal')
  await expect(page.getByText('The agent is driving', { exact: true })).toBeVisible()
  // The rendered Log out button: a press while the agent drives must not reach it.
  await clickRemote(page, 80, 365, phone)
  await expect(page.getByText('The agent is driving. Take over to use the page.', { exact: true })).toBeVisible()
  await fixture.waitText('Signed in as test@example.invalid')
  await page.getByRole('button', { name: 'Take over', exact: true }).click()
  await expect(driving(page)).toBeVisible()
  await expect(fixture.agent('browser', 'action', { ...request, text: 'STALE AGENT MUST NOT WRITE' })).rejects.toThrow()
  await fixture.waitText('Note: Written by the run principal')
  expect((await fixture.currentPage()).page_id).toBe(before.page_id)
}
