// The terminal dock's own tools, against a real environment container: the
// dock is closed until asked for, the font zoom holds across a reload, and
// the find bar searches what the shell actually printed.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { OnboardingWizard } from './pages/wizard'

test.skip(!dockerReachable(), 'the environment terminal needs a reachable Docker daemon')

test('the terminal dock opens on request, zooms and finds', async ({ page, aether }) => {
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

  // Closed on arrival: the board keeps the window until a terminal is asked
  // for, and the header strip is the only thing the dock spends it on.
  const dock = page.getByRole('region', { name: 'Terminal dock' })
  await expect(dock.getByRole('button', { name: 'Expand terminal dock' })).toBeVisible()
  await expect(dock.getByText('Your environment starts on first open')).toBeHidden()

  await dock.getByRole('button', { name: 'Expand terminal dock' }).click()
  await dock.getByRole('button', { name: 'Open', exact: true }).click()
  await expect(dock.getByRole('status')).toBeHidden({ timeout: 60_000 })

  const screen = dock.locator('.xterm-screen')
  await screen.click()
  // Ctrl+Shift+V must stay on xterm's native paste path. This exercises the
  // Windows-sensitive shortcut without depending on navigator.clipboard.readText
  // in the renderer.
  await page.context().grantPermissions(['clipboard-write'], {
    origin: new URL(alice.url).origin,
  })
  await page.evaluate(async () => {
    await navigator.clipboard.writeText('printf "\\141ether-native-paste\\n"\n')
  })
  await page.keyboard.press('Control+Shift+V')
  const nativePasteCount = async () => {
    const text = (await dock.locator('.xterm-rows').textContent()) ?? ''
    return text.split('aether-native-paste').length - 1
  }
  await expect.poll(nativePasteCount, { timeout: 30_000 }).toBe(1)
  await page.keyboard.type('echo aether-found-me\n')
  await expect(dock.locator('.xterm-rows')).toContainText('aether-found-me', {
    timeout: 30_000,
  })

  // Zoom moves the live terminal and is remembered, so a reload comes back
  // at the size the member left.
  const fontSize = () =>
    dock.locator('.xterm-rows').evaluate((el) => getComputedStyle(el).fontSize)
  expect(await fontSize()).toBe('12px')
  await page.keyboard.press('Control+Equal')
  await page.keyboard.press('Control+Equal')
  await expect.poll(fontSize).toBe('14px')

  await page.reload()
  await dock.getByRole('button', { name: 'Expand terminal dock' }).click()
  // The reattach's spinner has not necessarily mounted yet, so wait for the
  // rows the size is read off rather than for the spinner to go.
  await expect(dock.locator('.xterm-rows')).toBeVisible({ timeout: 60_000 })
  await expect.poll(fontSize).toBe('14px')

  // Find selects the match the shell printed; a term that is not there says
  // so rather than failing silently.
  await dock.locator('.xterm-screen').click()
  await page.keyboard.press('Control+Shift+F')
  const find = dock.getByLabel('Find in terminal')
  await find.fill('aether-found-me')
  await find.press('Enter')
  await expect(dock.locator('.xterm-selection div').first()).toBeVisible()

  await find.press('Escape')
  await expect(dock.getByLabel('Find in terminal')).toBeHidden()
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], {
    origin: new URL(alice.url).origin,
  })
  const readClipboard = () => page.evaluate(() => navigator.clipboard.readText())
  await dock.getByRole('button', { name: 'Copy terminal selection' }).click()
  await expect.poll(readClipboard).toBe('aether-found-me')

  // The keyboard copy path must copy xterm's selection even though the
  // browser has no native DOM selection to handle. Focus xterm directly so
  // the find input does not consume the shortcut or clear the match.
  await page.evaluate(() => navigator.clipboard.writeText('stale clipboard'))
  await dock.locator('.xterm-helper-textarea').focus()
  await page.keyboard.press(process.platform === 'darwin' ? 'Meta+C' : 'Control+Shift+C')
  await expect.poll(readClipboard).toBe('aether-found-me')

  if (process.platform === 'darwin') {
    // Native Cmd+C can arrive after xterm has lost focus (for example, after
    // a repaint). The document fallback must still use the selected terminal.
    await page.evaluate(() => navigator.clipboard.writeText('stale clipboard'))
    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
    })
    await expect
      .poll(() =>
        page.evaluate(
          () => document.activeElement === document.body || document.activeElement === document.documentElement,
        ),
      )
      .toBeTruthy()
    await page.keyboard.press('Meta+C')
    await expect.poll(readClipboard).toBe('aether-found-me')
  }

  await dock.locator('.xterm-helper-textarea').focus()
  await page.keyboard.press('Control+Shift+F')
  await find.fill('no-such-output')
  await find.press('Enter')
  await expect(dock.getByText('No matches')).toBeVisible()

  await find.press('Escape')
  await expect(dock.getByLabel('Find in terminal')).toBeHidden()

  if (process.platform === 'darwin') {
    // Enable xterm mouse reporting, then hold Option while dragging. The
    // configured macOptionClickForcesSelection path must keep this local,
    // rather than sending a mouse event to the shell.
    await dock.locator('.xterm-helper-textarea').focus()
    await page.keyboard.type("printf '\\033[?1000h'; printf '\\141ether-mouse-select\\n'\n")
    await expect(dock.locator('.xterm-rows')).toContainText('aether-mouse-select', {
      timeout: 30_000,
    })
    try {
      const row = dock
        .locator('.xterm-rows > div')
        .filter({ hasText: 'aether-mouse-select' })
        .last()
      const box = await row.boundingBox()
      expect(box).not.toBeNull()
      if (!box) throw new Error('mouse-selection row has no bounding box')
      const y = box.y + box.height / 2
      await page.keyboard.down('Alt')
      try {
        await page.mouse.move(box.x + 2, y)
        await page.mouse.down()
        await page.mouse.move(box.x + box.width - 2, y)
        await page.mouse.up()
      } finally {
        await page.mouse.up().catch(() => {})
        await page.keyboard.up('Alt').catch(() => {})
      }
      await expect(dock.locator('.xterm-selection div').first()).toBeVisible()
      await dock.getByRole('button', { name: 'Copy terminal selection' }).click()
      await expect.poll(readClipboard).toContain('aether-mouse-select')
    } finally {
      await dock.locator('.xterm-helper-textarea').focus()
      await page.keyboard.type("printf '\\033[?1000l'\n")
    }
  }

  await dock.getByRole('button', { name: 'Collapse terminal dock' }).click()
  await dock.getByRole('button', { name: 'Expand terminal dock' }).click()
  await expect(dock.locator('.xterm-rows')).toBeVisible({ timeout: 60_000 })
  await dock.locator('.xterm-screen').click()
  // Require new shell output rather than matching the echoed command.
  await page.keyboard.type('printf "\\141ether-dock-resumed\\n"\n')
  await expect(dock.locator('.xterm-rows')).toContainText('aether-dock-resumed', {
    timeout: 30_000,
  })
})
