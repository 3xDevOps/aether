// What the terminal tab says while it is waiting out a missing PTY session.
// A server or `aether gui` restart produces both halves at once: the socket
// drops while the server is down, and the attach that finally lands hits
// recovery still re-creating the session, which the gateway answers with
// -32004 rather than holding open. The client retries that on purpose, so
// the wait must not read as a failure while it is working.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

/**
 * Sockets dropped before the first refusal. Four leaves the reconnect counter
 * one short of the point `retry()` calls a connection offline, so the drops
 * themselves stay honest and the refusal after them is the one that would
 * report the deliberate wait as a failure.
 */
const drops = 4

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a run whose session is missing never reads as offline while it waits', async ({
  page,
  aether,
}) => {
  // Records every change of the connection label, from before the app's first
  // frame, because each state lasts only as long as one backoff.
  await page.addInitScript(() => {
    const seen: string[] = []
    ;(window as Window & { seenStates?: string[] }).seenStates = seen
    const labels = ['Connecting', 'Reconnecting', 'Attached', 'Offline']
    setInterval(() => {
      for (const span of document.querySelectorAll('span')) {
        const text = (span.textContent ?? '').trim()
        if (labels.includes(text) && seen[seen.length - 1] !== text) seen.push(text)
      }
    }, 30)
  })

  // The server going away and coming back, as the client sees it: sockets
  // that drop, then the refusal recovery answers with while it is still
  // re-creating the session. The socket after that is left unanswered, so the
  // run never reaches the honest refusal and the recording can be read while
  // the wait is still on.
  let opened = 0
  await page.routeWebSocket(/\/ws\/attach\//, (ws) => {
    opened++
    if (opened <= drops) {
      ws.close({ code: 1011 })
      return
    }
    if (opened <= drops + 1) {
      ws.send(JSON.stringify({ ok: false, code: -32004, error: 'ptyhost: no session for run' }))
      ws.close({ code: 1008, reason: 'attach refused' })
    }
  })

  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  // An agent that outlives the test, because the wait under test only applies
  // while the run can still gain a session: the seed repository's own script
  // exits at once and would take the run to needs-attention with it.
  aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  await alice.api.rpc('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'a run whose session is late',
  })

  await page.goto(alice.url)
  await page.getByRole('complementary').getByRole('button', { name: /session is late/ }).click()
  // The wait only applies to a run that can still gain a session, so the test
  // is only about what it says while the run is one.
  await expect(
    page.locator('header').filter({ hasText: 'a run whose session is late' }),
  ).toContainText('Working')

  // Every dropped socket, the refusal, and the reconnect that refusal earns.
  await expect(async () => {
    expect(opened).toBeGreaterThan(drops + 1)
  }).toPass({ timeout: 60_000 })

  const seen = await page.evaluate(
    () => (window as Window & { seenStates?: string[] }).seenStates ?? [],
  )
  expect(seen).toContain('Reconnecting')
  expect(seen).not.toContain('Offline')

  // The wait is the whole of what is on screen: no refusal to read, and
  // nothing to press that could not help.
  await expect(page.getByText('ptyhost: no session for run')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Retry' })).toHaveCount(0)
})
