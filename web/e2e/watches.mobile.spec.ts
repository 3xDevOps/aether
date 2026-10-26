// Watches on a phone against a real server: Watch… from a run's menu, the
// detail as a full-screen sheet, pause, resume and delete, and a notice the
// server recorded while the page was reloading.

import { expect, test } from './mobile'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a phone creates, pauses, resumes and deletes a watch, and sees its notice after a reload', async ({ page, aether }, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'mock',
    mode: 'acp',
    task: 'demo',
  })

  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('log', { name: 'Session' }).getByText('pong')).toBeVisible({ timeout: 60_000 })
  await page.getByRole('toolbar', { name: 'Run actions' }).getByRole('button', { name: 'More', exact: true }).tap()
  await page.getByRole('menuitem', { name: 'Watch…' }).tap()
  const form = page.getByRole('dialog', { name: /^Watch / })
  await form.getByRole('button', { name: 'Watch', exact: true }).tap()
  await expect(form).toBeHidden()
  const receipt = page.getByText(/^Watching run .* for: needs you, finished\./)
  await expect(receipt).toBeVisible()
  await testInfo.attach('watch receipt', { body: await page.screenshot(), contentType: 'image/png' })
  await page.getByRole('button', { name: 'View watch' }).tap()

  const detail = page.getByRole('dialog')
  await expect(detail).toBeVisible()
  const box = await detail.boundingBox()
  const viewport = page.viewportSize()!
  expect(box && Math.round(box.width)).toBe(viewport.width)
  await detail.getByRole('button', { name: 'Pause' }).tap()
  await expect(detail.getByText('Paused', { exact: true })).toBeVisible()
  await detail.getByRole('button', { name: 'Resume' }).tap()
  await expect(detail.getByText('Watching', { exact: true })).toBeVisible()

  // The agent asks while the page reloads; the notice is the server's, not the tab's.
  await alice.api.rpc('run.inject', { run_id: run.id, message: 'ask permission', idempotency_key: `watch-mobile-${run.id}` })
  await page.reload()
  const notices = page.getByRole('dialog').getByRole('region', { name: 'Notifications' })
  await expect(notices.getByText('No push device - in-app only').first()).toBeVisible({ timeout: 60_000 })
  await testInfo.attach('watch notice', { body: await page.screenshot(), contentType: 'image/png' })

  await page.getByRole('dialog').getByRole('button', { name: 'Delete…' }).tap()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete watch' }).tap()
  await expect(page.getByRole('heading', { name: 'No watches yet.' })).toBeVisible()
  const { watches } = await alice.api.rpc<{ watches: unknown[] }>('watch.list')
  expect(watches).toHaveLength(0)
})
