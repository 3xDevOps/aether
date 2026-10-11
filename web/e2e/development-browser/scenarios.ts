import type { Page } from '@playwright/test'
import { expect } from '../fixtures'
import type { DevBrowserActionResult, DevBrowserSnapshotResult, DevControlAcquireResult } from '../../src/lib/types'
import { addressBar, appAddress, browserAction, chooseViewport, clickRemote, expectLive, remotePage, remotePoint, typeRemote, type BrowserFixture } from './fixture'

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

/** A person marks up the page the agent is driving. Nothing here presses the
 * page or takes its lease: the real companion describes the element, the
 * dashboard crops it from the frame, and one message carries both. */
async function annotateForAgent(page: Page, fixture: BrowserFixture, phone: boolean): Promise<void> {
  const toggle = page.getByRole('button', { name: 'Annotate', exact: true })
  // A phone's toolbar has no room for the toggle until it is on.
  if (await toggle.count()) await toggle.click()
  else await browserAction(page, 'Annotate')
  await expect(toggle).toHaveAttribute('aria-pressed', 'true')
  if (!phone) {
    const point = await remotePoint(page, 80, 365)
    await page.mouse.move(point.x, point.y)
    await expect(page.locator('[data-slot="annotate-hover"]')).toHaveText('button#logout 300 × 40')
  }
  await clickRemote(page, 80, 365, phone)
  await page.getByRole('textbox', { name: 'Note 1', exact: true }).fill('Make this button red.')
  await page.getByRole('button', { name: 'Comment', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Note 1: button "Log out"', exact: true })).toBeVisible()

  // The wrong password earlier is this page's one failed request.
  const logs = page.getByRole('button', { name: 'Console and network', exact: true })
  if (await logs.count()) await logs.click()
  else {
    await page.getByRole('button', { name: 'Browser actions', exact: true }).click()
    await page.getByRole('menuitem', { name: /^Console and network/ }).click()
  }
  const strip = page.getByRole('region', { name: 'Console and network', exact: true })
  await strip.getByRole('listitem').filter({ hasText: `POST http://${appAddress}/login` }).getByRole('checkbox').click()

  const bar = page.getByRole('group', { name: 'Feedback for the agent', exact: true })
  await expect(bar).toContainText('1 annotation, 1 failed request')
  await bar.getByRole('button', { name: 'Send to agent', exact: true }).click()
  await expect(bar.getByRole('status')).toContainText('1 annotation, 1 failed request')
  const { workspaces } = await fixture.member.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { messages } = await fixture.member.api.rpc<{ messages: { kind: string; body: string }[] }>('run.room.list', { workspace_id: workspaces[0].id, run_id: fixture.runID })
  const body = messages.find((message) => message.kind === 'steer_request')?.body ?? ''
  expect(body).toContain('1. button "Log out"\nMake this button red.\n- Screenshot: /')
  expect(body).toMatch(/- Screenshot: \S+\/\.aether\/terminal-images\/\S+\n- Selector: button#logout\n/)
  expect(body).toContain(`POST http://${appAddress}/login 401`)

  await strip.getByRole('button', { name: 'Close', exact: true }).click()
  await toggle.click()
  // A phone's toolbar drops the toggle again once the mode is off.
  if (phone) await expect(toggle).toHaveCount(0)
  else await expect(toggle).not.toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByText('The agent is driving', { exact: true })).toBeVisible()
  await fixture.waitText('Signed in as test@example.invalid')
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
  await annotateForAgent(page, fixture, phone)
  await page.getByRole('button', { name: 'Take over', exact: true }).click()
  await expect(driving(page)).toBeVisible()
  await expect(fixture.agent('browser', 'action', { ...request, text: 'STALE AGENT MUST NOT WRITE' })).rejects.toThrow()
  await fixture.waitText('Note: Written by the run principal')
  expect((await fixture.currentPage()).page_id).toBe(before.page_id)
}
