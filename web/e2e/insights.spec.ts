// Insights counts what the real server recorded when a real run ended, so
// the finished run here is one the fake harness actually ran to completion.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the run Insights counts needs a reachable Docker daemon')

test('Insights counts a finished run and keeps it across period and scope', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const task = 'write the insights fixture'
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    mode: 'headless',
    task,
  })
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: run.id })).run.status, {
      timeout: 180_000,
      intervals: [250, 500, 1_000, 2_000],
    })
    .toBe('completed')

  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Insights', exact: true }).click()

  const mine = page.getByRole('region', { name: 'My statistics' })
  const team = page.getByRole('region', { name: 'Team statistics' })
  for (const section of [mine, team]) {
    await expect(section.getByRole('article', { name: 'Runs / swarms completed' })).toContainText('1 run / 0 swarms', { timeout: 30_000 })
    await expect(section.getByRole('article', { name: 'Success rate' })).toContainText('0%')
    await expect(section.getByRole('article', { name: 'Total tokens used' })).toBeVisible()
    await expect(section.getByRole('img')).toHaveCount(7)
    await expect(section.getByRole('img', { name: /runs 1 done/ })).toHaveCount(1)
  }
  await expect(team.getByRole('list', { name: 'Members' }).getByRole('listitem')).toHaveCount(1)
  await expect(mine.getByRole('list', { name: 'Members' })).toHaveCount(0)

  const work = page.getByRole('region', { name: 'Finished work' })
  await expect(work.getByRole('listitem')).toHaveCount(1)
  await expect(work.getByRole('listitem')).toContainText(task)
  await expect(work.getByRole('listitem')).toContainText('Completed')
  await expect(page.getByRole('main').getByRole('alert')).toHaveCount(0)
  expect(errors).toEqual([])
})

const empty = { done: 0, failed: 0, abandoned: 0 }

/** insights.get for the seven local days ending today, with alice and bob. */
function week(scope: string) {
  const days = Array.from({ length: 7 }, (_, i) => {
    const d = new Date()
    d.setDate(d.getDate() - 6 + i)
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  })
  const mineRuns = [{ done: 2, failed: 0, abandoned: 0 }, empty, { done: 3, failed: 1, abandoned: 0 }, { done: 1, failed: 0, abandoned: 1 }, empty, { done: 4, failed: 2, abandoned: 0 }, { done: 1, failed: 0, abandoned: 0 }]
  const bobRuns = [empty, { done: 2, failed: 0, abandoned: 0 }, { done: 1, failed: 1, abandoned: 1 }, empty, { done: 3, failed: 0, abandoned: 0 }, { done: 2, failed: 1, abandoned: 0 }, { done: 5, failed: 0, abandoned: 2 }]
  const bucket = (runs: typeof mineRuns, start: string, i: number) => ({ start, runs: runs[i], swarms: i === 6 ? { done: 1, failed: 0, abandoned: 0 } : empty })
  const sum = (runs: typeof mineRuns) => runs.reduce((a, r) => ({ done: a.done + r.done, failed: a.failed + r.failed, abandoned: a.abandoned + r.abandoned }), empty)
  const add = (a: typeof empty, b: typeof empty) => ({ done: a.done + b.done, failed: a.failed + b.failed, abandoned: a.abandoned + b.abandoned })
  const alice = days.map((d, i) => bucket(mineRuns, d, i))
  const bob = days.map((d, i) => bucket(bobRuns, d, i))
  const everyone = scope === 'everyone'
  const runs = everyone ? add(sum(mineRuns), sum(bobRuns)) : sum(mineRuns)
  return {
    generated_at: new Date().toISOString(),
    from: new Date().toISOString(),
    to: new Date().toISOString(),
    time_zone: 'UTC',
    bucket: 'day',
    totals: { launched: 30, completed: 0, merged: 0, failed: 0, interrupted: 0, abandoned: 0, killed: 0, active: 0, finished: runs.done },
    previous: { launched: 0, completed: 0, merged: 0, failed: 0, interrupted: 0, abandoned: 0, killed: 0, active: 0, finished: 0 },
    series: days.map((start) => ({ start, launched: 0, completed: 0, merged: 0, failed: 0, interrupted: 0, abandoned: 0 })),
    heatmap: [],
    by_workspace: [],
    by_agent: [],
    swarms: { launched: 2, completed: 1, cancelled: 0, agents: 6, tasks_accepted: 5 },
    durations: { runs_measured: 0, median_run_seconds: 0, p90_run_seconds: 0, state_runs_measured: 0, median_working_seconds: 0, median_waiting_seconds: 0, median_response_seconds: 0 },
    cost: {
      metered_runs: 4, unmetered_runs: 1, cost_usd: 3, input_tokens: 90_000, output_tokens: 10_000, total_tokens: 100_000,
      by_agent: [
        { harness: 'claude', metered_runs: 3, unmetered_runs: 0, cost_usd: 2, input_tokens: 50_000, output_tokens: 6_000, total_tokens: 56_000 },
        { harness: 'codex', metered_runs: 1, unmetered_runs: 1, cost_usd: 1, input_tokens: 40_000, output_tokens: 4_000, total_tokens: 44_000 },
      ],
    },
    pull_requests: { opened: 0, merged: 0 },
    success: { runs: { succeeded: everyone ? 9 : 5, finished: runs.done + runs.failed + runs.abandoned }, swarms: { succeeded: 1, finished: 1 } },
    outcomes: {
      runs,
      swarms: { done: 1, failed: 0, abandoned: 0 },
      series: days.map((start, i) => ({ start, runs: everyone ? add(mineRuns[i], bobRuns[i]) : mineRuns[i], swarms: bucket(mineRuns, start, i).swarms })),
      members: everyone
        ? [
            { member_id: 'mem_alice', display_name: 'alice', color: '#e6194b', runs: sum(mineRuns), swarms: { done: 1, failed: 0, abandoned: 0 }, series: alice },
            { member_id: 'mem_bob', display_name: 'bob', color: '#3cb44b', runs: sum(bobRuns), swarms: empty, series: bob.map((b) => ({ ...b, swarms: empty })) },
          ]
        : [],
    },
    recent: [],
    needs_you: 0,
    coverage: { state_times_since: '', pull_requests_since: '' },
  }
}

test('Insights dims the other days while one is hovered, for me and for the team', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  await page.route('**/api/v1/insights.get', (route) => route.fulfill({ json: week(route.request().postDataJSON().scope) }))
  await page.setViewportSize({ width: 1280, height: 1900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Insights', exact: true }).click()

  const mine = page.getByRole('region', { name: 'My statistics' })
  const team = page.getByRole('region', { name: 'Team statistics' })
  await expect(mine.getByRole('article', { name: 'Runs / swarms completed' })).toContainText('11 runs / 1 swarm', { timeout: 30_000 })
  await expect(mine.getByRole('article', { name: 'Total tokens used' })).toContainText(/used 56% of the 100K tokens/)
  await expect(team.getByRole('article', { name: 'Runs / swarms completed' })).toContainText('24 runs / 1 swarm')

  for (const section of [mine, team]) {
    const days = section.getByRole('img')
    await expect(days).toHaveCount(7)
    await days.nth(2).hover()
    await expect(days.nth(2)).toHaveAttribute('data-active', 'true')
    await expect(section.locator('[data-dimmed]')).toHaveCount(6)
    await expect(section.getByRole('tooltip')).toBeVisible()
    const opacity = (index: number) => days.nth(index).evaluate((node) => getComputedStyle(node).opacity)
    await expect.poll(() => opacity(0)).toBe('0.4')
    expect(await opacity(2)).toBe('1')
    await section.getByRole('button', { name: 'Swarms' }).click()
    await expect(section.getByText('1 swarm ended in the last 7 days')).toBeVisible()
    await section.getByRole('button', { name: 'Runs' }).click()
  }
  await expect(team.getByRole('list', { name: 'Members' })).toContainText('bob')
  await page.mouse.move(0, 0)
  await expect(page.locator('[data-dimmed]')).toHaveCount(0)
  await page.screenshot({ path: test.info().outputPath('insights.png'), fullPage: true })
})

test.describe('on a phone in the dark', () => {
  test.use({ colorScheme: 'dark', viewport: { width: 360, height: 900 } })

  test('Insights fits the width and keeps every box and bar readable', async ({ page, aether }) => {
    const alice = await aether.member('alice')
    await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
    await page.route('**/api/v1/insights.get', (route) => route.fulfill({ json: week(route.request().postDataJSON().scope) }))
    await page.goto(`${alice.url}${alice.url.includes('?') ? '&' : '?'}page=insights`)

    const team = page.getByRole('region', { name: 'Team statistics' })
    await expect(team.getByRole('article', { name: 'Total tokens used' })).toBeVisible({ timeout: 30_000 })
    await expect(team.getByRole('img')).toHaveCount(7)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await team.scrollIntoViewIfNeeded()
    await page.screenshot({ path: test.info().outputPath('insights-phone-dark.png'), fullPage: true })
  })
})
