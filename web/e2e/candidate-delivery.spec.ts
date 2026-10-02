// A real candidate review over two retained ordinary runs. The server, Git
// repositories, fake-agent containers, verification runtime, and delivery
// receipt are all real; only fixture setup uses the public gateway API.

import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import path from 'node:path'
import type { Locator, Page } from '@playwright/test'
import type { Candidate, CandidateSummary } from '../src/lib/integration-types'

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

const terminalTimeout = 3 * 60 * 1000
const alphaTask = 'candidate alpha file'
const betaTask = 'candidate beta file'
const alphaPath = 'alpha.txt'
const betaPath = 'beta.txt'
const alphaContent = 'alpha-from-run'
const betaContent = 'beta-from-run'
const conflictLeftPath = 'conflict-left.txt'
const conflictRightPath = 'conflict-right.txt'
const conflictOneLeft = 'conflict-one-left'
const conflictOneRight = 'conflict-one-right'
const conflictTwoLeft = 'conflict-two-left'
const conflictTwoRight = 'conflict-two-right'

test.skip(!dockerReachable(), 'a candidate verification needs a reachable Docker daemon')

type API = {
  rpc<T>(method: string, params?: unknown): Promise<T>
  local<T>(verb: string, params?: unknown): Promise<T>
}

type RunPacket = {
  id: string
  run_id: string
  trigger: string
  availability: string
  base_revision: string
  retained_revision: string
  changed_files?: { path: string }[]
}


type CandidateReviewSurface = {
  room: Locator
  evidence: Locator
  review: Locator
}

function git(repo: string, ...args: string[]): string {
  return execFileSync(
    'git',
    ['-C', repo, '-c', 'user.name=E2E', '-c', 'user.email=e2e@example.invalid', ...args],
    { encoding: 'utf8' },
  ).trim()
}

/**
 * The server's fake harness runs this committed script without a task argv.
 * The real run branch is the stable fixture input that keeps the two ordinary
 * runs non-conflicting while preserving one target base revision.
 */
function installCandidateAgent(repo: string): void {
  writeFileSync(
    path.join(repo, 'agent.sh'),
    `#!/bin/sh
set -eu
sleep 1
echo agent-ready
head=$(cat .git/HEAD)
branch=\${head#ref: refs/heads/}
case "$branch" in
  *candidate-alpha*) printf '%s\\n' '${alphaContent}' > ${alphaPath} ;;
  *candidate-beta*) printf '%s\\n' '${betaContent}' > ${betaPath} ;;
  *) echo "unexpected candidate branch: $branch" >&2; exit 1 ;;
esac
`,
    { mode: 0o755 },
  )
  git(repo, 'add', 'agent.sh')
  git(repo, 'commit', '-q', '-m', 'candidate delivery fixture agent')
}


function installConflictAgent(repo: string): void {
  writeFileSync(
    path.join(repo, 'agent.sh'),
    `#!/bin/sh
set -eu
sleep 1
echo agent-ready
head=$(cat .git/HEAD)
branch=\${head#ref: refs/heads/}
case "$branch" in
  *conflict-one*) left='${conflictOneLeft}'; right='${conflictOneRight}' ;;
  *conflict-two*) left='${conflictTwoLeft}'; right='${conflictTwoRight}' ;;
  *) echo "unexpected conflict branch: $branch" >&2; exit 1 ;;
esac
printf '%s\\n' "$left" > ${conflictLeftPath}
printf '%s\\n' "$right" > ${conflictRightPath}
`,
    { mode: 0o755 },
  )
  git(repo, 'add', 'agent.sh')
  git(repo, 'commit', '-q', '-m', 'candidate conflict fixture agent')
}
async function waitForCompletedRun(api: API, runID: string): Promise<void> {
  await expect
    .poll(
      async () => {
        const { run } = await api.rpc<{ run: { status: string } }>('run.get', { run_id: runID })
        return run.status
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('completed')
}

async function waitForFinishPacket(api: API, workspaceID: string, runID: string): Promise<RunPacket> {
  let packet: RunPacket | undefined
  await expect
    .poll(
      async () => {
        const { packets } = await api.rpc<{ packets: RunPacket[] }>('run.evidence.list', {
          workspace_id: workspaceID,
          run_id: runID,
          limit: 50,
        })
        packet = packets.find((item) => item.trigger === 'finish' && item.availability === 'available')
        return packet?.id ?? ''
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toMatch(/^[^/\\]+$/)
  if (!packet) throw new Error(`finish evidence did not remain available for ${runID}`)
  return packet
}

async function showCandidate(api: API, workspaceID: string, candidateID: string): Promise<Candidate> {
  const result = await api.rpc<{ candidate: Candidate }>('integration.show', {
    workspace_id: workspaceID,
    candidate_id: candidateID,
  })
  return result.candidate
}

async function listCandidates(api: API, workspaceID: string): Promise<CandidateSummary[]> {
  const result = await api.rpc<{ candidates: CandidateSummary[] }>('integration.list', {
    workspace_id: workspaceID,
    limit: 100,
  })
  return result.candidates
}

async function openCandidateReview(page: Page): Promise<CandidateReviewSurface> {
  await page.getByRole('button', { name: 'Open Run Room' }).click()
  const room = page.getByRole('complementary', { name: 'Run Room' })
  await expect(room).toBeVisible()
  const evidence = room.getByRole('region', { name: 'Run evidence' })
  await evidence.getByRole('button', { name: /^Evidence(?: \(\d+\))?$/ }).click()
  await expect(evidence.getByRole('button', { name: /^finish capture/ })).toBeVisible({
    timeout: terminalTimeout,
  })
  await evidence.getByRole('button', { name: 'Candidate review', exact: true }).click()
  const review = evidence.getByRole('region', { name: 'Candidate review' })
  await expect(review).toBeVisible()
  return { room, evidence, review }
}

test('reviews two retained runs, verifies them in a real container, and lands the approved candidate', async ({
  page,
  aether,
}, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  installCandidateAgent(repo)
  const expectedTargetRevision = await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspaceID = workspaces[0].id

  const { run: alphaRun } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaceID,
    harness: 'fake',
    task: alphaTask,
    mode: 'headless',
  })
  await waitForCompletedRun(alice.api, alphaRun.id)
  const alphaPacket = await waitForFinishPacket(alice.api, workspaceID, alphaRun.id)

  const { run: betaRun } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaceID,
    harness: 'fake',
    task: betaTask,
    mode: 'headless',
  })
  await waitForCompletedRun(alice.api, betaRun.id)
  const betaPacket = await waitForFinishPacket(alice.api, workspaceID, betaRun.id)

  expect(alphaPacket.base_revision).toBe(expectedTargetRevision)
  expect(betaPacket.base_revision).toBe(expectedTargetRevision)
  expect(alphaPacket.retained_revision).toMatch(/^[0-9a-f]{40}$/)
  expect(betaPacket.retained_revision).toMatch(/^[0-9a-f]{40}$/)
  expect(alphaPacket.changed_files?.map((file) => file.path)).toContain(alphaPath)
  expect(betaPacket.changed_files?.map((file) => file.path)).toContain(betaPath)

  await page.goto(`${alice.url}&run=${alphaRun.id}`)
  await expect(page.getByRole('heading', { name: alphaTask, exact: true })).toBeVisible()
  let { review } = await openCandidateReview(page)


  await review.getByRole('checkbox', { name: `Select packet ${alphaPacket.id}`, exact: true }).check()
  await review.getByRole('checkbox', { name: `Select packet ${betaPacket.id}`, exact: true }).check()
  await expect(review.getByLabel('Target ref')).toHaveValue('refs/heads/main')
  await expect(review.getByLabel('Expected target revision')).toHaveValue(expectedTargetRevision)
  await review.getByRole('button', { name: 'Prepare candidate', exact: true }).click()

  let candidateID = ''
  await expect
    .poll(
      async () => {
        const candidates = await listCandidates(alice.api, workspaceID)
        candidateID = candidates[0]?.candidate_id ?? ''
        return candidateID
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toMatch(/^[^/\\]+$/)
  let candidate = await showCandidate(alice.api, workspaceID, candidateID)
  expect(candidate.state).toBe('frozen')
  expect(candidate.target_ref).toBe('refs/heads/main')
  expect(candidate.expected_target_revision).toBe(expectedTargetRevision)
  expect(candidate.candidate_revision).toMatch(/^[0-9a-f]{40}$/)
  const candidateRevision = candidate.candidate_revision!

  await expect(review).toContainText(candidateRevision)
  await review.getByRole('button', { name: 'Load combined patch', exact: true }).click()
  await expect(review).toContainText(alphaPath)
  await expect(review).toContainText(betaPath)
  await expect(review).toContainText(`+${alphaContent}`)
  await expect(review).toContainText(`+${betaContent}`)

  // A source-mutating command is observed and fenced before any delivery gate.
  const mutatingArgv = JSON.stringify(['sh', '-c', `printf tampered > ${alphaPath}`])
  await review.getByLabel('Verification argv').fill(mutatingArgv)
  await review.getByLabel('Timeout seconds').fill('30')
  await review.getByRole('button', { name: 'Run verification', exact: true }).click()
  await expect
    .poll(
      async () => {
        candidate = await showCandidate(alice.api, workspaceID, candidateID)
        return candidate.verifications.at(-1)?.status
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('source_changed')
  candidate = await showCandidate(alice.api, workspaceID, candidateID)
  const sourceChanged = candidate.verifications.at(-1)
  if (!sourceChanged) throw new Error('source-mutating verification did not persist')
  await expect(review).toContainText(/source[_ ]changed/i)
  await expect(
    alice.api.rpc('integration.request_delivery', {
      workspace_id: workspaceID,
      candidate_id: candidateID,
      candidate_revision: candidateRevision,
      verification_ids: [sourceChanged.verification_id],
      action: 'update_ref',
      idempotency_key: 'candidate-source-changed-delivery',
    }),
  ).rejects.toThrow()
  await expect(review.getByRole('button', { name: 'Request delivery', exact: true })).toBeDisabled()

  const provingCommand =
    `test -f ${alphaPath} && test -f ${betaPath} && ` +
    `test "$(cat ${alphaPath})" = '${alphaContent}' && ` +
    `test "$(cat ${betaPath})" = '${betaContent}' && echo candidate-files-present`
  await review.getByLabel('Verification argv').fill(JSON.stringify(['sh', '-c', provingCommand]))
  await review.getByRole('button', { name: 'Run verification', exact: true }).click()
  await expect
    .poll(
      async () => {
        candidate = await showCandidate(alice.api, workspaceID, candidateID)
        return candidate.verifications.at(-1)?.status
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('passed')
  candidate = await showCandidate(alice.api, workspaceID, candidateID)
  const passed = candidate.verifications.at(-1)
  if (!passed) throw new Error('file-presence verification did not persist')
  expect(passed.output).toContain('candidate-files-present')
  await review
    .getByRole('checkbox', { name: `Select verification ${passed.verification_id}`, exact: true })
    .check()
  await expect(review).toContainText(/passed/i)

  await expect(review.getByLabel('Delivery action')).toHaveValue('update_ref')
  await review.getByRole('button', { name: 'Request delivery', exact: true }).click()
  await expect
    .poll(
      async () => (await showCandidate(alice.api, workspaceID, candidateID)).delivery_request?.state,
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('pending')
  candidate = await showCandidate(alice.api, workspaceID, candidateID)
  const request = candidate.delivery_request
  if (!request) throw new Error('delivery request was not persisted')
  expect(request.candidate_revision).toBe(candidateRevision)
  expect(request.verification_ids).toContain(passed.verification_id)
  const requestVersion = request.request_version
  // An offline page cannot mutate the durable request. Reconnect and reload
  // before the human gate, so no stale page state can approve anything.
  await expect(review.getByRole('button', { name: 'Approve delivery', exact: true })).toBeEnabled()
  await page.context().setOffline(true)
  await expect(review.getByRole('button', { name: 'Approve delivery', exact: true })).toBeDisabled()
  await page.context().setOffline(false)
  await expect(review.getByRole('button', { name: 'Approve delivery', exact: true })).toBeEnabled()
  await expect
    .poll(
      async () => (await showCandidate(alice.api, workspaceID, candidateID)).delivery_request?.state,
      { timeout: 15_000, intervals: [250, 500, 1_000] },
    )
    .toBe('pending')
  await page.reload()
  await page.getByRole('region', { name: 'Done', exact: true })
    .getByRole('button', { name: alphaTask, exact: true }).click()
  await expect(page.getByRole('heading', { name: alphaTask, exact: true })).toBeVisible()
  ;({ review } = await openCandidateReview(page))
  await expect(review.getByRole('button', { name: 'Show full', exact: true })).toBeVisible()
  await review.getByRole('button', { name: 'Show full', exact: true }).click()
  await expect(review).toContainText(candidateRevision)
  await review.getByRole('button', { name: 'Approve delivery', exact: true }).click()
  await expect
    .poll(
      async () => (await showCandidate(alice.api, workspaceID, candidateID)).delivery_request?.state,
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('approved')
  candidate = await showCandidate(alice.api, workspaceID, candidateID)
  expect(candidate.delivery_request?.request_version).toBe(requestVersion)

  await review.getByRole('button', { name: 'Deliver candidate', exact: true }).click()
  await expect
    .poll(
      async () => (await showCandidate(alice.api, workspaceID, candidateID)).delivery_receipt?.result,
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('landed')
  candidate = await showCandidate(alice.api, workspaceID, candidateID)
  expect(candidate.delivery_request?.request_version).toBe(requestVersion)
  expect(candidate.delivery_receipt?.candidate_revision).toBe(candidateRevision)
  await expect(review).toContainText('Landed')
  await expect(review).toContainText(candidateRevision)

  const pushed = await alice.api.local<{ state: string; workspace_commit: string }>('repo.push', {
    workspace_id: workspaceID,
  })
  expect(pushed.state).toBe('behind')
  expect(pushed.workspace_commit).toBe(candidateRevision)

  await testInfo.attach('candidate delivery review and landed receipt', {
    body: await page.screenshot({ fullPage: true }),
    contentType: 'image/png',
  })

  // Reloading an already delivered candidate reopens its durable receipt. A
  // replay must not call deliver again or perform another Git transaction.
  let replayDeliverCalls = 0
  page.on('request', (requestEvent) => {
    if (requestEvent.url().includes('/api/v1/integration.deliver')) replayDeliverCalls += 1
  })
  await page.reload()
  await page.getByRole('region', { name: 'Done', exact: true })
    .getByRole('button', { name: alphaTask, exact: true }).click()
  await expect(page.getByRole('heading', { name: alphaTask, exact: true })).toBeVisible()
  ;({ review } = await openCandidateReview(page))
  await expect(review.getByRole('button', { name: 'Show full', exact: true })).toBeVisible()
  await review.getByRole('button', { name: 'Show full', exact: true }).click()
  await expect(review).toContainText('Landed')
  await expect(review).toContainText(candidateRevision)
  expect(replayDeliverCalls).toBe(0)
})

test('applies selected conflict resolutions without overwriting untouched files', async ({ page, aether }, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('conflict-project')
  installConflictAgent(repo)
  const expectedTargetRevision = await seedWorkspace(alice, aether.server.addr, repo, 'conflict-project')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspaceID = workspaces[0].id

  const { run: firstRun } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaceID,
    harness: 'fake',
    task: 'candidate conflict one',
    mode: 'headless',
  })
  await waitForCompletedRun(alice.api, firstRun.id)
  const firstPacket = await waitForFinishPacket(alice.api, workspaceID, firstRun.id)

  const { run: secondRun } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaceID,
    harness: 'fake',
    task: 'candidate conflict two',
    mode: 'headless',
  })
  await waitForCompletedRun(alice.api, secondRun.id)
  const secondPacket = await waitForFinishPacket(alice.api, workspaceID, secondRun.id)

  await page.goto(`${alice.url}&run=${firstRun.id}`)
  await expect(page.getByRole('heading', { name: 'candidate conflict one', exact: true })).toBeVisible()
  const { review } = await openCandidateReview(page)
  await review.getByRole('checkbox', { name: `Select packet ${firstPacket.id}`, exact: true }).check()
  await review.getByRole('checkbox', { name: `Select packet ${secondPacket.id}`, exact: true }).check()
  await expect(review.getByLabel('Target ref')).toHaveValue('refs/heads/main')
  await expect(review.getByLabel('Expected target revision')).toHaveValue(expectedTargetRevision)
  await review.getByRole('button', { name: 'Prepare candidate', exact: true }).click()

  let candidateID = ''
  await expect
    .poll(
      async () => {
        const candidates = await listCandidates(alice.api, workspaceID)
        candidateID = candidates[0]?.candidate_id ?? ''
        return candidateID
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toMatch(/^[^/\\]+$/)
  let candidate = await showCandidate(alice.api, workspaceID, candidateID)
  expect(candidate.state).toBe('conflicted')
  expect(candidate.conflicts).toEqual([conflictLeftPath, conflictRightPath])
  await expect(review.getByRole('textbox', { name: `Resolution for ${conflictLeftPath}`, exact: true })).toBeVisible()
  await expect(review.getByRole('textbox', { name: `Resolution for ${conflictRightPath}`, exact: true })).toBeVisible()

  const resolvedLeft = 'resolved left content'
  const resolvedRight = 'resolved right content'
  const leftDraft = review.getByRole('textbox', { name: `Resolution for ${conflictLeftPath}`, exact: true })
  const rightDraft = review.getByRole('textbox', { name: `Resolution for ${conflictRightPath}`, exact: true })
  await leftDraft.fill(resolvedLeft)
  await expect(review.getByRole('checkbox', { name: `Apply resolution for ${conflictLeftPath}`, exact: true })).toBeChecked()
  await expect(review.getByRole('checkbox', { name: `Apply resolution for ${conflictRightPath}`, exact: true })).not.toBeChecked()
  await testInfo.attach('candidate review with one selected conflict resolution', {
    body: await page.screenshot({ fullPage: true }),
    contentType: 'image/png',
  })
  await review.getByRole('button', { name: 'Apply resolutions', exact: true }).click()

  await expect
    .poll(
      async () => {
        candidate = await showCandidate(alice.api, workspaceID, candidateID)
        return candidate.conflicts
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toEqual([conflictRightPath])
  await expect(review).toContainText(conflictRightPath)
  await expect(rightDraft).toHaveValue('')
  expect(candidate.conflicts).toEqual([conflictRightPath])

  await rightDraft.fill(resolvedRight)
  await expect(review.getByRole('checkbox', { name: `Apply resolution for ${conflictRightPath}`, exact: true })).toBeChecked()
  await review.getByRole('button', { name: 'Apply resolutions', exact: true }).click()
  await expect
    .poll(
      async () => {
        candidate = await showCandidate(alice.api, workspaceID, candidateID)
        return candidate.state
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('frozen')
  expect(candidate.conflicts ?? []).toEqual([])

  await review.getByRole('button', { name: 'Load combined patch', exact: true }).click()
  await expect(review).toContainText(`+${resolvedLeft.trim()}`)
  await expect(review).toContainText(`+${resolvedRight.trim()}`)
})
