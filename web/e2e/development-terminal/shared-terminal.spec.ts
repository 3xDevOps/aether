import { readFileSync } from 'node:fs'
import path from 'node:path'
import type { Locator, Page } from '@playwright/test'
import type { DevControlStatusResult, DevTerminalListResult, DevTerminalScreenshotResult } from '../../src/lib/types'
import { expect, test } from '../fixtures'
import { dockerReachable } from '../harness/server'
import { memberID, seedWorkspace } from '../harness/setup'

const tui = readFileSync(new URL('./querying-tui.sh', import.meta.url), 'utf8')
// The executable starts while the container is still provisioning. Discover
// live authority before issuing the one terminal-start mutation, just as an
// agent must; never retry a mutation whose outcome could be ambiguous.
const launch = `remaining=100
while :; do
  status=$(aether-internal status) || exit 1
  case "$status" in *'"dev.terminal.start"'*) break ;; esac
  remaining=$((remaining - 1))
  if [ "$remaining" -eq 0 ]; then printf '%s\\n' "$status" >&2; exit 1; fi
  sleep 0.1
done
printf '%s' '{"name":"agent-tui","command":["dev-tui"],"cols":73,"rows":19}' | aether-internal terminal start --params-file - || exit 1
while :; do sleep 1; done`

async function openDock(page: Page, url: string): Promise<Locator> {
  await page.goto(url)
  const sidebar = page.getByRole('button', { name: /^Open sidebar/ })
  await expect(sidebar.or(page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' }))).toBeVisible()
  if (await sidebar.isVisible()) {
    await sidebar.click()
    await page.getByRole('dialog', { name: 'Aether' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  } else {
    await page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  }
  await showShell(page, /agent-tui/)
  const dock = page.locator('[data-slot=shell-terminal]')
  await expect(dock.locator('.xterm-rows')).toContainText('Shared TUI: 界 λ é')
  return dock
}

/** Shell tabs are a strip on a desktop and one menu on a phone. */
async function showShell(page: Page, name: RegExp): Promise<void> {
  const main = page.getByRole('main')
  const tabs = main.getByRole('tablist', { name: 'Terminals' })
  const menu = main.getByRole('button', { name: /^Terminal: / })
  await expect(tabs.or(menu).first()).toBeVisible()
  if (await tabs.isVisible()) await tabs.getByRole('tab', { name }).click()
  else {
    await menu.click()
    await page.getByRole('menuitem', { name }).click()
  }
}

async function terminalActions(page: Page, dock: Locator): Promise<Locator> {
  await dock.getByRole('button', { name: 'Shell actions' }).click()
  return page.getByRole('menu')
}

test.skip(!dockerReachable(), 'requires a real Docker daemon and run container')

test('agent-created TUI shares authority, geometry, protocol responses, limits and process lifetime', async ({ page, browser, aether }, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('development-terminal')
  await seedWorkspace(alice, aether.server.addr, repo)
  const member = await memberID(alice)
  aether.installAgent(member, 'dev-tui', tui)
  aether.installAgent(member, 'claude', launch)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id, harness: 'claude', task: 'development terminal acceptance',
  })
  const list = () => alice.api.rpc<DevTerminalListResult>('dev.terminal.list', { run_id: run.id })
  await expect.poll(async () => (await list()).terminals.some((item) => item.name === 'agent-tui')).toBe(true)
  const terminal = (await list()).terminals.find((item) => item.name === 'agent-tui')!
  const target = { run_id: run.id, terminal_id: terminal.terminal_id, incarnation: terminal.incarnation }
  const surface = { kind: 'terminal', id: terminal.terminal_id, incarnation: terminal.incarnation }
  const status = () => alice.api.rpc<DevControlStatusResult>('dev.control.status', { run_id: run.id, surface })
  const home = aether.server.memberHome(member)
  const file = (name: string) => {
    try { return readFileSync(path.join(home, `development-tui-${name}`), 'utf8') } catch (error) {
      if (error instanceof Error && 'code' in error && error.code === 'ENOENT') return ''
      throw error
    }
  }
  await page.setViewportSize({ width: 1568, height: 1000 })
  const dock = await openDock(page, alice.url)
  expect((await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)).toMatchObject({ cols: 73, rows: 19, started_by: 'run_agent' })
  await expect(dock.getByRole('tab', { name: 'agent-tui, started by the agent' })).toBeVisible()
  // Watching takes nothing. Nobody drives this shell, so the first key takes
  // its lease and arrives with the one typed behind it.
  expect((await status()).controller).toBeNull()
  await expect(dock.getByRole('button', { name: 'Take control' })).toBeHidden()
  const input = dock.locator('.xterm-helper-textarea')
  await expect(input).toBeFocused()
  await page.keyboard.type('XQ')
  await expect(dock.getByRole('button', { name: 'Release' })).toBeVisible()
  const firstController = (await status()).controller!
  expect(firstController.kind).toBe('member')
  await expect.poll(() => file('input').slice(0, 2)).toBe('XQ')
  await expect.poll(() => file('effects')).toBe('effect\n')
  await expect(dock.locator('.xterm-rows')).toContainText('Side effect written')
  await expect.poll(() => (file('input').match(/\x1b\[[?\d;]*c/g) ?? []).length).toBe(1)
  await expect.poll(() => (file('input').match(/\x1b\[[\d;]*R/g) ?? []).length).toBe(1)
  await expect.poll(() => file('input').includes('rgb:')).toBe(true)
  await dock.locator('.xterm-screen').click({ position: { x: 40, y: 12 } })
  await expect.poll(() => /\x1b\[<0;\d+;\d+M/.test(file('input'))).toBe(true)
  await input.focus()
  await page.keyboard.insertText('λ界')
  await expect.poll(() => file('input').includes('λ界')).toBe(true)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.evaluate(() => navigator.clipboard.writeText('paste-界'))
  await page.keyboard.press('Control+Shift+V')
  await expect.poll(() => file('input').includes('paste-界')).toBe(true)
  await testInfo.attach('shared-alternate-screen', { body: await dock.screenshot(), contentType: 'image/png' })
  await expect(dock.getByText('You control', { exact: true })).toBeVisible()

  const watcherContext = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const watcherPage = await watcherContext.newPage()
  try {
    const beforeWatch = (await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)!
    const watcher = await openDock(watcherPage, alice.url)
    await watcherPage.setViewportSize({ width: 360, height: 740 })
    expect((await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)).toMatchObject({ cols: beforeWatch.cols, rows: beforeWatch.rows })
    // Someone holds it now, so typing cannot take it: the phone names the
    // holder and asks before displacing them.
    await expect(watcher.getByText(/ controls this shell\.$/)).toBeVisible()
    await watcher.getByRole('button', { name: 'Take control' }).click()
    await watcherPage.getByRole('dialog', { name: 'Take control of this shell?' })
      .getByRole('button', { name: 'Take control' }).click()
    await expect(watcher.getByRole('button', { name: 'Release' })).toBeVisible()
    await expect(dock.getByText(/ controls$/)).toBeVisible()
    await expect(dock.getByRole('button', { name: 'Take control' })).toBeVisible()
    await expect(alice.api.rpc('dev.terminal.input', { ...target, control_session_id: firstController.control_session_id, control_generation: firstController.control_generation, kind: 'text', text: 'X' })).rejects.toThrow()
    expect(file('effects')).toBe('effect\n')
    await watcher.locator('.xterm-helper-textarea').focus()
    await watcherPage.keyboard.type('X')
    await expect.poll(() => file('effects')).toBe('effect\neffect\n')
    await watcher.getByRole('button', { name: 'Release' }).click()
    await expect.poll(async () => (await status()).controller).toBeNull()
    await expect(watcher.getByRole('button', { name: 'Take control' })).toBeHidden()
    await (await terminalActions(watcherPage, watcher))
      .getByRole('menuitem', { name: 'Hide, keep running' }).click()
    const heartbeat = file('heartbeat').length
    await expect.poll(() => file('heartbeat').length).toBeGreaterThan(heartbeat)
    await showShell(watcherPage, /Show agent-tui/)
    await expect(watcher.locator('.xterm-rows')).toContainText('Side effect written')
    expect(file('starts')).toBe('start\n')
    expect((await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)?.incarnation).toBe(terminal.incarnation)
  } finally { await watcherContext.close() }

  // The agent's shell does not count against the four a run's people get.
  for (let index = 0; index < 4; index++) {
    await alice.api.rpc('dev.terminal.start', { run_id: run.id, name: `capacity-${index}`, command: ['sleep', '600'] })
  }
  await expect(alice.api.rpc('dev.terminal.start', { run_id: run.id, name: 'over-capacity' })).rejects.toThrow(/people have 4 shells running/)
  const newShell = dock.getByRole('button', { name: 'New shell' })
  await expect(newShell).toHaveAttribute('aria-disabled', 'true')
  await expect(dock.getByText('At most 4 shells besides the agent’s')).toBeVisible()
  // The way out is on the tab: stopping one frees its place, and the ended
  // shell gives way to the next one started.
  await dock.getByRole('button', { name: 'Close capacity-0' }).click()
  await page.getByRole('menuitem', { name: 'Stop shell' }).click()
  await expect.poll(async () => (await list()).terminals.find((item) => item.terminal_id === 'capacity-0')?.process.state).toBe('stopped')
  await expect(dock.getByRole('tab', { name: /capacity-0/ })).toBeHidden()
  await expect(newShell).not.toHaveAttribute('aria-disabled', 'true')
  await alice.api.rpc('dev.terminal.start', { run_id: run.id, name: 'after-stop', command: ['sleep', '600'] })
  expect((await list()).terminals.map((item) => item.terminal_id)).not.toContain('capacity-0')

  // The phone released, so the desktop types its way back in.
  await expect(dock.getByText(/ controls$/)).toBeHidden()
  await expect(dock.getByRole('button', { name: 'Take control' })).toBeHidden()
  await input.focus()
  await page.keyboard.type('X')
  await expect(dock.getByRole('button', { name: 'Release' })).toBeVisible()
  await expect.poll(() => file('effects')).toBe('effect\neffect\neffect\n')
  // A first capture includes companion launch and has a bounded 90s API
  // lifecycle; do not abort that request at the ordinary 30s locator limit.
  const captureResponse = page.waitForResponse((response) => response.url().endsWith('/api/v1/dev.terminal.screenshot'), { timeout: 95_000 })
  await (await terminalActions(page, dock))
    .getByRole('menuitem', { name: 'Take a screenshot', exact: true }).click()
  const captured = await captureResponse
  expect(captured.ok(), await captured.text()).toBe(true)
  const { artifact } = await captured.json() as DevTerminalScreenshotResult
  expect(artifact).toMatchObject({ source: 'terminal', run_id: run.id, terminal_id: terminal.terminal_id, incarnation: terminal.incarnation })
  const image = await fetch(new URL(`/api/v1/dev/${run.id}/artifacts/${artifact.id}`, alice.url), {
    headers: { authorization: `Bearer ${new URL(alice.url).searchParams.get('token')}` },
    signal: AbortSignal.timeout(30_000),
  })
  expect(image.status).toBe(200)
  expect(image.headers.get('content-type')).toBe('image/png')
  await testInfo.attach('shared-terminal-capture', { body: Buffer.from(await image.arrayBuffer()), contentType: 'image/png' })
  await testInfo.attach('shared-terminal-capture-metadata', { body: JSON.stringify(artifact), contentType: 'application/json' })
  await expect(dock.getByRole('status').filter({ hasText: /Captured/ })).toBeVisible()
  await (await terminalActions(page, dock))
    .getByRole('menuitem', { name: 'Stop shell' }).click()
  await expect.poll(async () => (await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)?.process.state).not.toBe('running')
  // A stopped shell does not come back as a tab.
  await page.reload()
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  const tabs = page.getByRole('main').getByRole('tablist', { name: 'Terminals' })
  await expect(tabs.getByRole('tab', { name: 'after-stop' })).toBeVisible()
  await expect(tabs.getByRole('tab', { name: /agent-tui/ })).toHaveCount(0)
  expect(file('starts')).toBe('start\n')
  expect((file('input').match(/\x1b\[[?\d;]*c/g) ?? []).length).toBe(1)
})
