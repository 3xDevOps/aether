// The Home tree in Files through a real browser, gateway and server: a file
// is created, edited, renamed, uploaded over and deleted in the member's
// persistent home, and each step is read back from the server's disk. A
// credential file is listed and never opened.

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

test('Files creates, renames, uploads and deletes in the remote home', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const home = aether.server.memberHome(await memberID(alice))
  const onDisk = (rel: string) => path.join(home, rel)
  mkdirSync(onDisk('.claude'), { recursive: true })
  writeFileSync(onDisk('.claude/.credentials.json'), '{}\n')

  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Files', exact: true }).click()
  const tree = page.getByRole('complementary', { name: 'Files' })
  const row = (name: string) => tree.getByRole('button', { name, exact: true })
  const item = (name: string) => page.getByRole('menuitem', { name, exact: true })
  const viewer = page.getByRole('article')

  const homeRow = tree.getByRole('button', { name: /^Home\s?~$/ })
  await expect(homeRow).toHaveAttribute('aria-expanded', 'false')
  await homeRow.click()
  await expect(row('.claude')).toBeVisible()

  // New folder from the row's own button, then a file inside it by right click.
  await homeRow.hover()
  await tree.getByRole('button', { name: 'Home actions', exact: true }).click()
  await item('New folder').click()
  await tree.getByRole('textbox', { name: 'New folder name' }).fill('notes')
  await page.keyboard.press('Enter')
  await expect(row('notes')).toBeFocused()
  await row('notes').click({ button: 'right' })
  await item('New file').click()
  const fileName = tree.getByRole('textbox', { name: 'New file name' })
  await fileName.fill('todo.md')
  await page.keyboard.press('Enter')
  await expect(viewer.locator('header')).toContainText('~/notes/todo.md')
  expect(readFileSync(onDisk('notes/todo.md'), 'utf8')).toBe('')

  await viewer.locator('.cm-content').click()
  await page.keyboard.type('first line')
  await viewer.getByRole('button', { name: 'Save', exact: true }).click()
  await expect.poll(() => readFileSync(onDisk('notes/todo.md'), 'utf8')).toBe('first line')

  // The open tab follows the rename, with the edit that was not saved.
  await viewer.locator('.cm-content').click()
  await page.keyboard.type(' and a draft')
  await row('todo.md').focus()
  await page.keyboard.press('F2')
  await expect(tree.getByRole('textbox', { name: 'New name for todo.md' })).toBeFocused()
  await page.keyboard.type('done')
  await page.keyboard.press('Enter')
  await expect(row('done.md')).toBeFocused()
  await expect(viewer.locator('header')).toContainText('~/notes/done.md')
  await expect(viewer.locator('.cm-content')).toContainText('first line and a draft')
  expect(existsSync(onDisk('notes/todo.md'))).toBe(false)
  expect(readFileSync(onDisk('notes/done.md'), 'utf8')).toBe('first line')

  // An upload keeps the file's bytes, and an existing file is replaced only on request.
  const upload = async (bytes: number[]) => {
    await row('notes').click({ button: 'right' })
    const chooser = page.waitForEvent('filechooser')
    await item('Upload files…').click()
    await (await chooser).setFiles({ name: 'logo.bin', mimeType: 'application/octet-stream', buffer: Buffer.from(bytes) })
  }
  await upload([0, 255, 137, 80])
  await expect(row('logo.bin')).toBeVisible()
  expect([...readFileSync(onDisk('notes/logo.bin'))]).toEqual([0, 255, 137, 80])
  await upload([1, 2, 3])
  const replace = page.getByRole('alertdialog', { name: 'Replace 1 file?' })
  await expect(replace).toContainText('logo.bin')
  expect([...readFileSync(onDisk('notes/logo.bin'))]).toEqual([0, 255, 137, 80])
  await replace.getByRole('button', { name: 'Replace', exact: true }).click()
  await expect(replace).toBeHidden()
  await expect.poll(() => [...readFileSync(onDisk('notes/logo.bin'))]).toEqual([1, 2, 3])

  // A file dropped on a folder's row lands in that folder.
  const dropped = await page.evaluateHandle(() => {
    const data = new DataTransfer()
    data.items.add(new File(['dropped'], 'dropped.txt'))
    return data
  })
  await row('notes').dispatchEvent('dragover', { dataTransfer: dropped })
  await row('notes').dispatchEvent('drop', { dataTransfer: dropped })
  await expect(row('dropped.txt')).toBeVisible()
  expect(readFileSync(onDisk('notes/dropped.txt'), 'utf8')).toBe('dropped')

  // A credential file is listed, and opening it shows the server's refusal.
  await row('.claude').click()
  await tree.getByRole('button', { name: /^\.credentials\.json\s?Credential file$/ }).click()
  await expect(viewer.getByRole('alert')).toHaveText(
    'home.read: .claude/.credentials.json is a credential file; read it in the Environment terminal',
  )

  // Delete asks first, names what is lost, and closes the tab under the folder.
  await row('notes').focus()
  await page.keyboard.press('Delete')
  const remove = page.getByRole('alertdialog', { name: 'Delete notes?' })
  await expect(remove).toContainText('~/notes and everything in it will be deleted.')
  await expect(remove).toContainText('An open file in it has unsaved edits, which will be discarded.')
  expect(existsSync(onDisk('notes'))).toBe(true)
  await remove.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(remove).toBeHidden()
  await expect(row('notes')).toHaveCount(0)
  await expect(page.getByRole('tab', { name: /done\.md/ })).toHaveCount(0)
  expect(existsSync(onDisk('notes'))).toBe(false)
})
