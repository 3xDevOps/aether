// The assistant's auto-approval toggle against a real gateway and server: the
// section says what the setting covers, the warning comes before the setting
// changes, the server stores it, and the dashboard reads it back after a
// reload. Answering the suggestion goes through the same server method.

import { expect, test } from './fixtures'

test('auto-approval is turned on only after the warning and is stored on the server', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const settings = (params?: Record<string, unknown>) =>
    alice.api.rpc<{ auto_approve: boolean; suggest_auto_approve: boolean; suggestion_off: boolean }>('assistant.settings', params)

  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Settings', exact: true }).click()
  const toggle = page.getByRole('checkbox', { name: 'Let the assistant act without asking' })
  await expect(toggle).toBeEnabled()
  await expect(toggle).not.toBeChecked()
  const scope = page.getByLabel('What auto-approval covers')
  await expect(scope).toContainText('Launching runs, messaging and stopping your own runs, saving your environment')
  await expect(scope).toContainText('Messaging or stopping another member’s run.')
  await expect(scope).toContainText('The assistant’s own shell and file tools')
  expect(await settings({ suggestion: 'later' })).toEqual({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true })

  await toggle.click()
  const dialog = page.getByRole('alertdialog', { name: 'Let the assistant act without asking?' })
  await expect(dialog).toContainText('launch runs, message and stop your runs and save your environment on its own')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(toggle).not.toBeChecked()
  expect((await settings()).auto_approve).toBe(false)

  await toggle.click()
  await dialog.getByRole('button', { name: 'Turn on' }).click()
  await expect(dialog).toBeHidden()
  await expect(toggle).toBeChecked()
  expect((await settings()).auto_approve).toBe(true)

  await page.reload()
  await expect(toggle).toBeChecked()

  await toggle.click()
  await expect(dialog).toBeHidden()
  await expect(toggle).not.toBeChecked()
  await expect.poll(async () => (await settings()).auto_approve).toBe(false)
})
