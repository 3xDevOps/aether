// Dictation in a session composer against a real gateway and server. The
// server has no speech model here, so server.info is answered with the
// dictation feature and dictation.transcribe with fixed text; Chromium's fake
// microphone supplies the audio. The run is one the server never launched,
// added to run.list and run.get, so the composer renders without Docker.

import { expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

test.use({
  launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] },
  permissions: ['microphone'],
})

const runID = 'run-dictation'

test('a dictated message lands at the caret of the session composer, unsent', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const id = await memberID(alice)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string; name: string }[] }>('workspace.list')
  const now = new Date().toISOString()
  const run = {
    id: runID,
    workspace_id: workspaces[0].id,
    member_id: id,
    account_member_id: id,
    task: 'tidy the importer',
    title: 'Tidy the importer',
    harness: 'claude',
    mode: 'tui',
    status: 'running',
    state: 'working',
    status_seq: 1,
    branch: 'aether/run-dictation',
    created_at: now,
    started_at: now,
    finished_at: null,
  }

  await page.route('**/api/v1/server.info', async (route) => {
    const info = await (await route.fetch()).json()
    await route.fulfill({ json: { ...info, features: [...(info.features ?? []), 'dictation'] } })
  })
  await page.route('**/api/v1/run.list', async (route) => {
    const list = await (await route.fetch()).json()
    await route.fulfill({ json: { ...list, runs: [...(list.runs ?? []), run] } })
  })
  await page.route('**/api/v1/run.get', async (route) => {
    if (route.request().postDataJSON()?.run_id !== runID) return route.fallback()
    await route.fulfill({ json: { run } })
  })
  const uploads: { audio_base64: string; hints: string[] }[] = []
  await page.route('**/api/v1/dictation.transcribe', async (route) => {
    uploads.push(route.request().postDataJSON())
    if (uploads.length > 1) return route.fulfill({ status: 503, json: { error: { code: -32004, message: 'dictation is busy: 2 requests queued; try again' } } })
    await route.fulfill({ json: { text: 'and keep the old parser' } })
  })
  const sent: string[] = []
  await page.route(/\/api\/v1\/run\.(inject|room\.post)$/, async (route) => {
    sent.push(route.request().url())
    await route.abort()
  })

  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto(`${alice.url}&run=${runID}`)
  await page.getByRole('tab', { name: 'Chat' }).click()
  const box = page.getByRole('textbox', { name: 'Message the agent' })
  await box.fill('Rewrite the parser then ship it')
  await box.evaluate((el: HTMLTextAreaElement) => el.setSelectionRange(18, 18))

  await box.focus()
  await page.getByRole('button', { name: 'Dictate', pressed: false }).click()
  const stop = page.getByRole('button', { name: 'Dictate', pressed: true })
  await expect(stop).toBeVisible()
  await expect(page.getByRole('status').filter({ hasText: /^Recording/ })).toBeAttached()
  // The press leaves focus in the box, so a phone's keyboard stays up, and Enter cannot send mid-recording.
  await expect(box).toBeFocused()
  await box.press('Enter')
  // Long enough for the fake device to deliver a few worklet chunks.
  await page.waitForTimeout(1_000)
  await stop.click()

  await expect(box).toHaveValue('Rewrite the parser and keep the old parser then ship it')
  await expect(box).toBeFocused()
  expect(await box.evaluate((el: HTMLTextAreaElement) => el.selectionStart)).toBe('Rewrite the parser and keep the old parser'.length)
  const wav = Buffer.from(uploads[0].audio_base64, 'base64')
  expect(wav.toString('ascii', 0, 4)).toBe('RIFF')
  expect(wav.readUInt16LE(22)).toBe(1)
  expect(wav.readUInt32LE(24)).toBe(16_000)
  expect(wav.readUInt32LE(40)).toBeGreaterThan(16_000)
  expect(uploads[0].hints).toEqual(expect.arrayContaining(['project', 'Tidy the importer']))

  // A failure shows the server's words above the box and leaves the draft and focus where they were.
  await page.getByRole('button', { name: 'Dictate', pressed: false }).click()
  await page.waitForTimeout(500)
  await page.getByRole('button', { name: 'Dictate', pressed: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'dictation is busy: 2 requests queued; try again' })).toBeVisible()
  await expect(box).toBeFocused()
  await expect(box).toHaveValue('Rewrite the parser and keep the old parser then ship it')
  expect(uploads).toHaveLength(2)
  expect(sent).toEqual([])
})
