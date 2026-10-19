// The Brief panel prints what the real server's memory.brief answers, so a
// note pinned from the form shows there only once the server stored it and
// fitted the pin into its budget.

import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

test('a note added and pinned on the Memory page enters the brief', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Memory', exact: true }).click()

  const title = 'Use bun in web/; never npm.'
  await page.getByRole('toolbar', { name: 'Memory actions' }).getByRole('button', { name: 'Add note' }).click()
  const form = page.getByRole('dialog', { name: 'Add a note' })
  await form.getByLabel('Kind').click()
  await page.getByRole('option', { name: 'Convention' }).click()
  await form.getByLabel('Note').fill(`${title}\nThe lockfile is bun.lock.`)
  await form.getByRole('checkbox', { name: 'Pin' }).check()
  await form.getByRole('button', { name: 'Add note' }).click()
  await expect(form).toBeHidden()

  const note = page.getByRole('list', { name: 'Notes' }).getByRole('article').filter({ hasText: title })
  await expect(note).toContainText('Pinned by you')
  await expect(note).toContainText('Convention')
  await expect(note).toContainText('Member note')

  await page.getByRole('button', { name: /^Brief/ }).click()
  const brief = page.getByRole('region', { name: 'Brief' }).locator('pre')
  await expect(brief).toContainText(/- note-\w+ convention \(.+\): Use bun in web\/; never npm\./)
  const { text } = await alice.api.rpc<{ text: string }>('memory.brief', { workspace_id: workspaces[0].id })
  expect(await brief.textContent()).toBe(text)
  expect(errors).toEqual([])
})
