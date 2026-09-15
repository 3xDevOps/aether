// Finish evidence is a retained, inspectable record rather than a status
// decoration. This scenario lets the real fake harness exit, waits for the
// server's terminal lifecycle and durable packet, then reads every source
// through the Run Room after ordinary runtime cleanup has had a chance to run.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

const task = 'retain the finished run evidence'
const terminalTimeout = 3 * 60 * 1000

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

/** Poll only the public run API, and always stop at a bounded deadline. */
async function waitForCompletedRun(
  member: { api: { rpc<T>(method: string, params?: unknown): Promise<T> } },
  runID: string,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const { run } = await member.api.rpc<{ run: { status: string } }>('run.get', {
          run_id: runID,
        })
        return run.status
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('completed')
}

/** Wait until the finish packet is durable before handing the run to the UI. */
async function waitForFinishEvidence(
  member: { api: { rpc<T>(method: string, params?: unknown): Promise<T> } },
  workspaceID: string,
  runID: string,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const { packets } = await member.api.rpc<{
          packets: Array<{ trigger: string }>
        }>('run.evidence.list', {
          workspace_id: workspaceID,
          run_id: runID,
          limit: 50,
        })
        return packets.some((packet) => packet.trigger === 'finish')
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe(true)
}

test('retains finish evidence after the run is cleaned up', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task,
    mode: 'headless',
  })

  // The seed repository's fake agent emits agent-ready, writes result.txt,
  // and exits. The server commits the result, captures evidence, and only
  // then releases the ordinary run container, checkout, and transcript.
  await waitForCompletedRun(alice, run.id)
  await waitForFinishEvidence(alice, workspaces[0].id, run.id)

  // Use the same tokened dashboard deep link as the desktop shell. Opening it
  // only after the lifecycle settles means the assertions exercise retained
  // sources, not the live checkout or PTY session.
  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Open Run Room' }).click()
  const room = page.getByRole('complementary', { name: 'Run Room' })
  await expect(room).toBeVisible()

  const evidence = room.getByRole('region', { name: 'Run evidence' })
  await evidence.getByRole('button', { name: /^Evidence(?: \(\d+\))?$/ }).click()
  await expect(evidence.getByRole('button', { name: /^finish capture/ })).toBeVisible({
    timeout: terminalTimeout,
  })

  // Select the automatically generated finish packet and inspect its factual
  // summary, including the changed file and each source's availability.
  await evidence.getByRole('button', { name: /^finish capture/ }).click()
  await expect(evidence.getByRole('heading', { name: 'finish capture', exact: true })).toBeVisible()
  await expect(evidence).toContainText(task)
  await expect(evidence.getByRole('heading', { name: 'Changed files', exact: true })).toBeVisible()
  await expect(evidence.getByText('result.txt', { exact: true })).toBeVisible()
  await expect(evidence.getByRole('heading', { name: 'Source availability', exact: true })).toBeVisible()
  await expect(evidence).toContainText(/git: available/)
  await expect(evidence).toContainText(/transcript: available/)
  await expect(evidence).toContainText(/event_log: available/)
  await expect(evidence).toContainText(/\d+\/\d+ sources available/)
  await expect(evidence).toContainText('Recorded observations, not verification')

  // Patch bytes come from the retained Git evidence revision, not the run's
  // checkout, so result.txt must remain readable after cleanup.
  await evidence.getByRole('tab', { name: 'Patch', exact: true }).click()
  await expect(evidence.getByText('result.txt', { exact: false })).toBeVisible()
  await expect(evidence.getByText('+hello-from-agent', { exact: false })).toBeVisible()
  await expect(evidence).not.toContainText('Patch unavailable')

  // Transcript bytes are a separately retained source and must still expose
  // the fake agent's real terminal output.
  await evidence.getByRole('tab', { name: 'Transcript', exact: true }).click()
  await expect(evidence.getByText('agent-ready', { exact: false })).toBeVisible()
  await expect(evidence).not.toContainText('Transcript unavailable')
})
