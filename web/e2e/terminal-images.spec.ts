import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

test.skip(!dockerReachable(), 'the environment terminal needs a reachable Docker daemon')

const imageBytes = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
  'base64',
)
const imageDigest = '431ced6916a2a21a156e38701afe55bbd7f88969fbbfc56d7fe099d47f265460'

test('uploads a chosen image and verifies it from the target shell', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.agents.skip().click()
  await page.getByRole('button', { name: 'Board', exact: true }).click()

  const dock = page.getByRole('region', { name: 'Terminal dock' })
  await dock.getByRole('button', { name: 'Expand terminal dock' }).click()
  await dock.getByRole('button', { name: 'Open', exact: true }).click()
  await expect(dock.getByRole('status')).toBeHidden({ timeout: 60_000 })

  const screen = dock.locator('.xterm-screen')
  await screen.click()
  await page.keyboard.type('sha256sum ')
  await dock.getByRole('button', { name: 'Upload image to terminal' }).click()
  await dock.locator('input[type=file]').setInputFiles({
    name: 'chosen.png',
    mimeType: 'image/png',
    buffer: imageBytes,
  })
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('chosen.png')

  const uploadResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith('/api/v1/terminal.image') &&
      response.request().method() === 'POST',
  )
  await dialog.getByRole('button', { name: 'Upload and insert' }).click()
  const response = await uploadResponse
  expect(response.ok()).toBe(true)
  const uploaded = (await response.json()) as { path: string }
  expect(uploaded.path).toMatch(/\/\.aether\/terminal-images\/image-[0-9a-f]+\.png$/)
  await expect(dialog).toBeHidden()

  // The image path is pasted, but upload never presses Enter. Seeing the
  // returned path in the echoed command proves insertion, while the digest
  // remains absent until the explicit Enter below.
  const rows = dock.locator('.xterm-rows')
  await expect(rows).toContainText(uploaded.path)
  await expect(rows).not.toContainText(imageDigest)
  await page.keyboard.press('Enter')
  await expect(rows).toContainText(imageDigest, { timeout: 30_000 })
})

test('targets image bytes at a live run shell', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const task = 'verify image bytes in a live run'
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task,
  })

  await page.goto(alice.url)
  await page.getByRole('complementary').getByRole('button', { name: new RegExp(task) }).click()
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  await expect(page.getByText('Attached')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByRole('button', { name: 'Steering' })).toBeVisible()

  const runDock = page.getByRole('region', { name: 'Terminal dock' })
  await runDock.getByRole('button', { name: 'Expand terminal dock' }).click()
  await expect(runDock.getByRole('button', { name: 'Open shell' })).toBeVisible({
    timeout: 60_000,
  })
  await runDock.getByRole('button', { name: 'Open shell' }).click()
  const screen = runDock.locator('.xterm-screen')
  await expect(screen).toBeVisible({ timeout: 60_000 })
  await expect(
    runDock.getByRole('button', { name: 'Upload image to terminal' }),
  ).toBeEnabled({ timeout: 60_000 })
  await screen.click()
  await page.keyboard.type('sha256sum ')
  await runDock.getByRole('button', { name: 'Upload image to terminal' }).click()
  await runDock.locator('input[type=file]').setInputFiles({
    name: 'run-target.png',
    mimeType: 'image/png',
    buffer: imageBytes,
  })
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('run-target.png')

  const uploadResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith('/api/v1/terminal.image') &&
      response.request().method() === 'POST',
  )
  await dialog.getByRole('button', { name: 'Upload and insert' }).click()
  const response = await uploadResponse
  expect(response.ok()).toBe(true)
  expect(response.request().postDataJSON()).toMatchObject({ run_id: run.id })
  const uploaded = (await response.json()) as { path: string }
  expect(uploaded.path).toMatch(/\/\.aether\/terminal-images\/image-[0-9a-f]+\.png$/)
  await expect(dialog).toBeHidden()

  const rows = runDock.locator('.xterm-rows')
  await expect(rows).toContainText(uploaded.path)
  await expect(rows).not.toContainText(imageDigest)
  await page.keyboard.press('Enter')
  await expect(rows).toContainText(imageDigest, { timeout: 30_000 })
})
