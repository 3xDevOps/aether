import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('captures stay tappable in a short phone sheet', async ({ page, aether }, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const task = 'inspect retained evidence on a phone'
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task,
    mode: 'headless',
  })
  await expect.poll(async () => {
    const { packets } = await alice.api.rpc<{ packets: { trigger: string }[] }>('run.evidence.list', {
      workspace_id: workspaces[0].id,
      run_id: run.id,
      limit: 50,
    })
    return packets.some((packet) => packet.trigger === 'finish')
  }, { timeout: 3 * 60 * 1000, intervals: [250, 500, 1_000, 2_000] }).toBe(true)

  await page.setViewportSize({ width: 390, height: 600 })
  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  const trigger = page.getByRole('button', { name: 'More', exact: true })
  await trigger.tap()
  await page.getByRole('menuitem', { name: 'Captures…' }).tap()
  const evidence = page.getByRole('dialog', { name: 'Captures', exact: true })
  await evidence.getByRole('button', { name: 'Open finish capture' }).tap()

  await evidence.getByRole('tab', { name: 'Patch', exact: true }).tap()
  await expect(evidence.getByText('+hello-from-agent', { exact: false })).toBeVisible()
  await evidence.getByRole('tab', { name: 'Summary', exact: true }).tap()
  await expect(evidence.getByText('result.txt', { exact: true })).toBeVisible()
  await testInfo.attach('short phone evidence sheet', {
    body: await page.screenshot({ fullPage: true }),
    contentType: 'image/png',
  })

  await evidence.getByRole('button', { name: 'Close', exact: true }).tap()
  await expect(evidence).toBeHidden()
  await expect(trigger).toBeFocused()
})
