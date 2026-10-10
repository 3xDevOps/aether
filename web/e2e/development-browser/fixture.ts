import { execFile } from 'node:child_process'
import { copyFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { promisify } from 'node:util'
import type { Locator, Page } from '@playwright/test'
import type { Aether, Member } from '../fixtures'
import { expect } from '../fixtures'
import { runContainer, removeContainers } from '../harness/docker'
import { seedWorkspace } from '../harness/setup'
import { waitFor } from '../harness/process'
import type { DevBrowserPage, DevBrowserPagesResult, DevBrowserSnapshotResult, DevBrowserWaitResult } from '../../src/lib/types'

const exec = promisify(execFile)
/** Typed without a scheme: the address bar adds `http://` for a loopback host. */
export const appAddress = '127.0.0.1:31873'

export interface BrowserFixture {
  member: Member
  runID: string
  appContainer: string
  agent: <T>(group: string, operation: string, params?: object) => Promise<T>
  currentPage: () => Promise<DevBrowserPage>
  snapshot: () => Promise<DevBrowserSnapshotResult>
  waitText: (text: string) => Promise<void>
  hotUpdate: () => Promise<void>
  stop: () => Promise<void>
}

export async function launchBrowserFixture(aether: Aether): Promise<BrowserFixture> {
  const member = await aether.member('browser-human')
  const repo = await aether.seedRepo('browser-app')
  await copyFile(path.resolve('e2e/development-browser/app.mjs'), path.join(repo, 'app.mjs'))
  await writeFile(path.join(repo, 'theme.mjs'), 'export const title = "Shared login app";\n')
  // The fake harness only holds a live run for the real browser/broker test.
  // It is not evidence of an authenticated vendor model/tool loop.
  await writeFile(path.join(repo, 'agent.sh'), 'echo agent-ready\nwhile :; do sleep 3600; done\n')
  await exec('git', ['-C', repo, 'add', 'app.mjs', 'theme.mjs', 'agent.sh'])
  await exec('git', ['-C', repo, '-c', 'user.name=E2E', '-c', 'user.email=e2e@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-m', 'real loopback login fixture'])
  await seedWorkspace(member, aether.server.addr, repo)
  const { workspaces } = await member.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await member.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, harness: 'fake', task: 'Shared browser verification' })
  const container = runContainer(run.id)
  const appContainer = `aether-e2e-browser-app-${run.id}`
  try {
    // No published ports. Node and the companion share only this run's
    // network namespace; the real app binds 127.0.0.1 inside it.
    await exec('docker', ['run', '--detach', '--name', appContainer, '--network', `container:${container}`, '--volumes-from', `${container}:ro`, '--workdir', '/workspace', 'node:22.14.0-alpine3.21', 'node', 'app.mjs'], { timeout: 120000 })
    await waitFor('run-loopback login app', async () => {
      const { stdout } = await exec('docker', ['logs', appContainer])
      return stdout.includes('loopback-login-ready')
    })
  } catch (cause) {
    await removeContainers([appContainer])
    throw cause
  }
  const agent = async <T>(group: string, operation: string, params: object = {}): Promise<T> => {
    const { stdout } = await exec('docker', ['exec', container, 'sh', '-c', 'printf %s "$1" | /usr/local/bin/aether-internal "$2" "$3" --params-file -', 'agent-command', JSON.stringify(params), group, operation], { timeout: 90000, maxBuffer: 128 * 1024 })
    const envelope = JSON.parse(stdout) as { ok: boolean; result: T; error?: { message: string } }
    if (!envelope.ok) throw new Error(envelope.error?.message ?? stdout)
    return envelope.result
  }
  const currentPage = async () => {
    const status = await member.api.rpc<{ session_id: string }>('dev.browser.status', { run_id: run.id })
    const result = await member.api.rpc<DevBrowserPagesResult>('dev.browser.pages', { run_id: run.id, session_id: status.session_id })
    const page = result.pages.find((item) => item.page_id === result.selected_page_id)
    if (!page) throw new Error('Shared browser has no selected page')
    return page
  }
  return {
    member, runID: run.id, appContainer, agent, currentPage,
    snapshot: async () => {
      const page = await currentPage()
      return member.api.rpc<DevBrowserSnapshotResult>('dev.browser.snapshot', { run_id: run.id, session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision, max_nodes: 128, max_chars: 8192 })
    },
    waitText: async (text) => {
      await expect.poll(async () => {
        try {
          const page = await currentPage()
          const result = await member.api.rpc<DevBrowserWaitResult>('dev.browser.wait', { run_id: run.id, session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision, condition: 'text', text, timeout_ms: 1000 })
          return result.matched ? text : 'Text not present in remote DOM'
        } catch (cause) { return String(cause) }
      }, { message: `remote DOM contains ${text}`, timeout: 90000 }).toBe(text)
    },
    hotUpdate: async () => {
      await exec('docker', ['exec', container, 'sh', '-c', 'printf \'export const title = "Updated without logout";\\n\' > /workspace/theme.mjs'])
    },
    stop: async () => { await removeContainers([appContainer]); await member.api.rpc('run.kill', { run_id: run.id }) },
  }
}

/** The Browser tab is there before any browser has started, with the address bar ready. */
export async function openBrowserPane(page: Page, fixture: BrowserFixture): Promise<void> {
  await page.goto(`${fixture.member.url}&run=${fixture.runID}`)
  await page.getByRole('tab', { name: 'Browser', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Open a page', exact: true })).toBeVisible()
  await expect(addressBar(page)).toBeEnabled()
}

export function addressBar(page: Page): Locator {
  return page.getByRole('textbox', { name: 'Address', exact: true })
}

export function remotePage(page: Page): Locator {
  return page.getByLabel('Shared browser page', { exact: true })
}

/** The canvas is busy until a frame of the current stream has been painted. */
export async function expectLive(page: Page): Promise<void> {
  await expect(remotePage(page)).toHaveAttribute('aria-busy', 'false')
}

/** Picks a viewport from the toolbar, or from Browser actions in a narrow pane, and waits for its frames. */
export async function chooseViewport(page: Page, name: 'Fit the pane' | 'Desktop' | 'Tablet' | 'Phone', width?: number): Promise<void> {
  const toolbar = page.getByRole('button', { name: 'Viewport', exact: true })
  await (await toolbar.count() ? toolbar : page.getByRole('button', { name: 'Browser actions', exact: true })).click()
  await page.getByRole('menuitemradio', { name: new RegExp(`^${name}`) }).click()
  if (width) await expect(remotePage(page)).toHaveAttribute('width', String(width))
}

export async function browserAction(page: Page, name: string): Promise<void> {
  await page.getByRole('button', { name: 'Browser actions', exact: true }).click()
  await page.getByRole('menuitem', { name, exact: true }).click()
}

/** Fixture layout coordinates are CSS pixels in the actual remote viewport. */
export async function clickRemote(page: Page, x: number, y: number, touch = false, clickCount = 1): Promise<void> {
  const canvas = remotePage(page)
  await expectLive(page)
  const geometry = await canvas.evaluate((element) => {
    const rect = element.getBoundingClientRect()
    return { left: rect.left, top: rect.top, width: rect.width, height: rect.height, frameWidth: (element as HTMLCanvasElement).width, frameHeight: (element as HTMLCanvasElement).height }
  })
  const scale = Math.min(1, geometry.width / geometry.frameWidth, geometry.height / geometry.frameHeight)
  const px = geometry.left + (geometry.width - geometry.frameWidth * scale) / 2 + x * scale
  const py = geometry.top + (geometry.height - geometry.frameHeight * scale) / 2 + y * scale
  // Focusable means this tab drives or nobody does, and then the press is sent.
  const driven = await canvas.getAttribute('tabindex') === '0'
  const completed = driven ? page.waitForResponse((response) => {
    if (!response.url().endsWith('/api/v1/dev.browser.action')) return false
    const input = response.request().postDataJSON()
    return input.action === (touch ? 'touch' : 'pointer') && input.phase === 'up' && (touch || input.click_count === clickCount)
  }) : null
  if (touch) await page.touchscreen.tap(px, py)
  else await page.mouse.click(px, py, { clickCount })
  if (completed) {
    const response = await completed
    expect(response.ok(), await response.text()).toBe(true)
  }
}

export async function typeRemote(page: Page, text: string, phone = false): Promise<void> {
  if (phone) {
    await page.getByRole('button', { name: 'Keyboard', exact: true }).click()
    const completed = page.waitForResponse((response) => response.url().endsWith('/api/v1/dev.browser.action') && response.request().postDataJSON().action === 'text')
    await page.keyboard.insertText(text)
    const response = await completed
    expect(response.ok(), await response.text()).toBe(true)
  } else {
    // Keep real physical key events, but wait for each admission rather than
    // outpacing a remote server with a fixed synthetic typing interval.
    for (const character of text) {
      const completed = page.waitForResponse((response) => response.url().endsWith('/api/v1/dev.browser.action') && response.request().postDataJSON().action === 'key')
      await page.keyboard.type(character)
      const response = await completed
      expect(response.ok(), await response.text()).toBe(true)
    }
  }
}
