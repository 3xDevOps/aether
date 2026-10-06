// A swarm from cancel to delete through the swarm page's More menu, and a
// display name changed in Profile. The integrator is a shell fixture.

import { expect, test } from './fixtures'
import { waitForCoordCLI } from './harness/coord'
import { memberID, seedWorkspace } from './harness/setup'

const launchTimeout = 3 * 60 * 1000

test('cancels, archives, unarchives and deletes a swarm from its page', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('swarm-lifecycle-project')
  await seedWorkspace(alice, aether.server.addr, repo, 'swarm-lifecycle-project')
  const aliceID = await memberID(alice)
  aether.installAgent(aliceID, 'claude', 'printf "scripted swarm fixture agent\\n"\nsleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspaceID = workspaces[0].id

  const objective = 'Swarm lifecycle fixture'
  const integrator = { account_member_id: aliceID, harness: 'claude', mode: 'tui' }
  const { mission } = await alice.api.rpc<{ mission: { id: string; current_integrator_run_id: string } }>('mission.create', {
    workspace_id: workspaceID,
    objective,
    accountable_human_id: aliceID,
    integrator,
    execution_choices: [{ ...integrator, mode: 'headless' }, integrator],
    idempotency_key: 'swarm-lifecycle-create',
  })
  const integratorRun = mission.current_integrator_run_id
  await waitForCoordCLI(integratorRun, aether.server.dataDir)

  const url = new URL(alice.url)
  url.searchParams.set('page', 'missions')
  url.searchParams.set('id', mission.id)
  await page.goto(url.toString())
  const more = page.getByRole('button', { name: 'More swarm actions' })
  const menuItem = async (name: string) => {
    await more.click()
    await page.getByRole('menuitem', { name }).click()
  }

  await expect(more).toBeVisible({ timeout: launchTimeout })
  await more.click()
  await expect(page.getByRole('menuitem', { name: 'Archive swarm…' })).toHaveCount(0)
  await expect(page.getByRole('menuitem', { name: 'Delete swarm…' })).toHaveCount(0)
  await page.getByRole('menuitem', { name: 'Cancel swarm…' }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel swarm' }).click()
  await expect(page.getByText('Swarm cancelled', { exact: true })).toBeVisible()
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: integratorRun })).run.status, { timeout: launchTimeout })
    .toMatch(/^(abandoned|failed|interrupted)$/)

  await menuItem('Archive swarm…')
  const archive = page.getByRole('alertdialog', { name: 'Archive this swarm?' })
  await archive.getByRole('button', { name: 'Archive swarm' }).click()
  await expect(page.getByText('Swarm archived', { exact: true })).toBeVisible()
  await expect(archive).toHaveCount(0)
  await expect(page.getByText(/· archived /)).toBeVisible({ timeout: 30_000 })
  const archivedRun = await alice.api.rpc<{ run: { archived_at?: string } }>('run.get', { run_id: integratorRun })
  expect(archivedRun.run.archived_at).toBeTruthy()

  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Swarms', exact: true }).click()
  await expect(page.getByRole('list', { name: 'Swarms' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Archived (1)' }).click()
  await page.getByRole('list', { name: 'Archived swarms' }).getByRole('button', { name: objective }).click()

  await menuItem('Unarchive swarm')
  await expect(page.getByText('Swarm restored', { exact: true })).toBeVisible()
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { archived_at?: string } }>('run.get', { run_id: integratorRun })).run.archived_at ?? '')
    .toBe('')

  await menuItem('Delete swarm…')
  await page.getByRole('alertdialog', { name: 'Delete this swarm?' }).getByRole('button', { name: 'Delete swarm' }).click()
  await expect(page.getByText('Swarm deleted', { exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'No swarms yet' })).toBeVisible()
  const { missions } = await alice.api.rpc<{ missions: unknown[] }>('mission.list', { workspace_id: workspaceID })
  expect(missions).toEqual([])
  await expect(alice.api.rpc('run.get', { run_id: integratorRun })).rejects.toThrow(/not found/)
})

test('renames the member from Profile and shows the new name everywhere', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('rename-project'))
  await page.goto(alice.url)

  const nav = page.getByRole('navigation', { name: 'Aether' })
  await nav.getByRole('button', { name: /, (Live|Reconnecting|Offline)$/ }).click()
  await page.getByRole('menuitem', { name: 'Profile' }).click()
  const profile = page.getByRole('dialog', { name: 'Profile' })
  const form = profile.getByRole('form', { name: 'Display name' })
  const field = form.getByLabel('Display name')
  await field.fill('x'.repeat(64) + 'y')
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(form.getByRole('alert')).toContainText('display_name is longer than 64 characters')
  await field.fill('   ')
  await expect(form.getByRole('button', { name: 'Save' })).toBeDisabled()
  await field.fill('Alice Cooper')
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText('Display name saved', { exact: true })).toBeVisible()
  await expect(profile.getByText('Alice Cooper', { exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(nav.getByRole('button', { name: /^Alice Cooper, / })).toBeVisible()
  const info = await alice.api.rpc<{ member: { display_name: string } }>('server.info')
  expect(info.member.display_name).toBe('Alice Cooper')
})
