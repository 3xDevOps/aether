import { execFile } from 'node:child_process'
import { rmSync } from 'node:fs'
import path from 'node:path'
import { promisify } from 'node:util'
import { expect, test } from '../fixtures'
import { runContainer } from '../harness/docker'
import { dockerReachable } from '../harness/server'
import { memberID } from '../harness/setup'
import type { RoomMessage, RunGitStatusResult, RunPRCreateResult, RunPRStatusResult, Workspace, WorkspaceImportResult, WorkspaceMirrorResult } from '../../src/lib/types'

const exec = promisify(execFile)
const baseRepository = process.env.AETHER_E2E_GITHUB_BASE_REPOSITORY
const headRepository = process.env.AETHER_E2E_GITHUB_HEAD_REPOSITORY
const baseBranch = process.env.AETHER_E2E_GITHUB_BASE_BRANCH ?? 'main'
const token = process.env.AETHER_E2E_GITHUB_TOKEN
const enabled = process.env.AETHER_E2E_GITHUB_PUBLISH === '1'

const nativeImage = process.env.AETHER_E2E_STANDARD_IMAGE ?? 'aether-standard:ci'
test.use({ serverOptions: { standardImage: nativeImage } })
let credentialPath: string | undefined

test.skip(!dockerReachable(), 'Requires Docker with the real Ubuntu Aether image.')
test.afterEach(() => {
  if (credentialPath) rmSync(credentialPath, { force: true })
  credentialPath = undefined
})
test.beforeAll(async () => {
  try {
    await exec('docker', ['image', 'inspect', nativeImage])
  } catch {
    throw new Error(`Required native Git image ${nativeImage} is missing. Build it from the repository root: docker build -f images/standard/Dockerfile -t ${nativeImage} .`)
  }
})
test.skip(!enabled || !token || !baseRepository || !headRepository, 'Real GitHub acceptance is opt-in: AETHER_E2E_GITHUB_PUBLISH=1, AETHER_E2E_GITHUB_TOKEN, public BASE_REPOSITORY and writable fork HEAD_REPOSITORY (both prefixed AETHER_E2E_GITHUB_), optional BASE_BRANCH. The token must push the fork and create/comment/close PRs upstream. This test creates then closes a PR and deletes its temporary head branch; no simulated success is accepted.')

test('publishes the reviewed fork head, discovers the exact PR, and sends only selected real feedback', async ({ page, aether }) => {
  test.setTimeout(600_000)
  const admin = await aether.member('GitHub publisher')
  await admin.api.local('link.apply', { addr: aether.server.addr, name: admin.name })
  const imported = await admin.api.rpc<WorkspaceImportResult>('workspace.import', {
    name: 'github-remote-only', environment: {}, source_url: `https://github.com/${baseRepository}.git`,
    base_branch: baseBranch, origin: `https://github.com/${headRepository}.git`, auth: 'public',
  })
  expect(imported.error).toBeFalsy()
  expect(imported.created).toBe(true)
  const workspaceID = imported.workspace.id
  await admin.api.rpc<WorkspaceMirrorResult>('workspace.mirror.adopt', { workspace_id: workspaceID, generation: imported.mirror.generation, expected_commit: imported.mirror.observed_commit })
  const account = await memberID(admin)
  aether.installAgent(account, 'claude', 'sleep 600')
  // Native gh stores this credential only in the disposable member home.
  // The token never appears in argv or attachments.
  const { run } = await admin.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaceID, harness: 'claude', task: 'remote GitHub fork acceptance' })
  await page.goto(admin.url)
  await page.getByRole('button', { name: 'remote GitHub fork acceptance · claude', exact: true }).click()
  const sourceBeforePublish = await admin.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: workspaceID })
  expect(sourceBeforePublish.source_url).toBe(`https://github.com/${baseRepository}.git`)
  expect(sourceBeforePublish.accepted_commit).toMatch(/^[a-f0-9]{40}$/)
  const container = runContainer(run.id)
  const initialHead = await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'rev-parse', 'HEAD'])
  expect(initialHead.stdout.trim()).toBe(sourceBeforePublish.accepted_commit)
  const ghPath = (await exec('docker', ['exec', container, 'sh', '-c', 'command -v gh'])).stdout.trim()
  expect(ghPath).toMatch(/^\/(?:usr\/)?(?:local\/)?bin\/gh$/)
  credentialPath = path.join(aether.server.memberHome(account), '.config', 'gh', 'hosts.yml')
  await new Promise<void>((resolve, reject) => {
    const child = execFile('docker', ['exec', '-i', container, 'gh', 'auth', 'login', '--hostname', 'github.com', '--git-protocol', 'https', '--with-token'], { timeout: 60_000 }, (error) => {
      if (error) reject(error)
      else resolve()
    })
    child.stdin?.end(token)
  })
  const headBranch = `aether-e2e-${crypto.randomUUID()}`
  const file = `${headBranch}.txt`
  const unrelatedFile = `${headBranch}-unrelated.txt`
  const branches = [headBranch, `${headBranch}-direct`, `${headBranch}-uncertain`, `${headBranch}-rejected`]
  const description = 'Automated remote-development acceptance; closed after inspection.\n\nThis PR was made with the help of omp.'
  await exec('docker', ['exec', '-w', '/workspace', container, 'sh', '-c', [
    'git config user.name "GitHub E2E"', 'git config user.email github-e2e@example.invalid', 'git config commit.gpgsign false',
    'gh auth setup-git --hostname github.com', `printf "reviewed remote change\\n" > ${file}`,
    `printf "unrelated staged change\\n" > ${unrelatedFile}`, `git add ${unrelatedFile}`,
  ].join(' && ')])
  const evidence: Record<string, unknown> = { baseRepository, headRepository, baseBranch, branches, runID: run.id }
  const targetFor = (branch: string) => ({ repository: baseRepository!, base_branch: baseBranch, head_repository: headRepository!, head_branch: branch })
  const nativeGH = async (args: string[]) => exec('docker', ['exec', '-w', '/workspace', container, ghPath, ...args])
  const pr = page.getByRole('region', { name: 'GitHub pull request' })
  const selectPR = async (branch: string, base = baseBranch, head = headRepository!) => {
    await pr.getByLabel('PR repository (owner/name)', { exact: true }).fill(baseRepository!)
    await pr.getByLabel('PR base branch', { exact: true }).fill(base)
    await pr.getByLabel('PR head repository (owner/name)', { exact: true }).fill(head)
    await pr.getByLabel('PR head branch', { exact: true }).fill(branch)
    await pr.getByRole('button', { name: 'Discover existing PR' }).click()
  }
  const createPR = async (branch: string) => {
    await pr.getByLabel('PR title', { exact: true }).fill(`Aether E2E ${branch}`)
    await pr.getByRole('textbox', { name: 'PR description', exact: true }).fill(description)
    await pr.getByRole('checkbox', { name: 'Draft PR', exact: true }).check()
    await pr.getByRole('checkbox', { name: 'I reviewed this GitHub identity and the exact PR repository, base, fork and head.' }).check()
    await pr.getByRole('button', { name: 'Create reviewed PR' }).click()
  }
  try {
    await page.getByRole('tab', { name: 'Changes', exact: true }).click()
    await page.getByRole('button', { name: 'Publish…', exact: true }).click()
    await page.getByRole('checkbox', { name: `Select ${file}`, exact: true }).check()
    await page.getByRole('button', { name: 'Review selected paths' }).click()
    await expect(page.getByRole('region', { name: `Untracked contents: ${file}` })).toContainText('reviewed remote change')
    await page.getByLabel('Commit message', { exact: true }).fill('Verify remote-only GitHub workflow')
    await page.getByRole('button', { name: 'Commit selected' }).click()
    await expect(page.getByRole('region', { name: 'Commit outcome' })).toContainText('Committed: yes')
    const committed = await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'diff-tree', '--no-commit-id', '--name-only', '-r', 'HEAD'])
    expect(committed.stdout.trim()).toBe(file)
    const staged = await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'diff', '--cached', '--name-only'])
    expect(staged.stdout.trim()).toBe(unrelatedFile)
    evidence.selectedCommitPaths = committed.stdout.trim()
    evidence.preservedStagedPath = staged.stdout.trim()
    await page.getByRole('tab', { name: '2 · Push and pull request' }).click()
    await page.getByRole('combobox', { name: 'Push remote', exact: true }).click()
    await page.getByRole('option', { name: 'origin', exact: true }).click()
    await page.getByRole('combobox', { name: 'Writable push URL', exact: true }).click()
    await page.getByRole('option', { name: `https://github.com/${headRepository}.git`, exact: true }).click()
    await page.getByLabel('Push head branch', { exact: true }).fill(headBranch)
    await page.getByRole('checkbox', { name: 'I reviewed the run account, branch, HEAD and exact push destination above.' }).check()
    await page.getByRole('button', { name: 'Push reviewed branch' }).click()
    await expect(page.getByRole('region', { name: 'Push outcome' })).toContainText('Pushed: yes')
    await selectPR(headBranch)
    await expect(pr).toContainText('No existing PR for this exact repository, base and head.')
    await createPR(headBranch)
    await expect(pr.getByRole('region', { name: 'PR creation outcome' })).toContainText('Created: yes')
    const status = await admin.api.rpc<RunGitStatusResult>('run.git.status', { run_id: run.id })
    const target = { repository: baseRepository!, base_branch: baseBranch, head_repository: headRepository!, head_branch: headBranch }
    const found = await admin.api.rpc<RunPRStatusResult>('run.pr.status', { run_id: run.id, expected: { branch: status.branch, head: status.head }, target })
    expect(found.error).toBeFalsy()
    expect(found.pull_request).toMatchObject({ ...target, head_oid: status.head })
    const prNumber = found.pull_request!.number
    evidence.publishedPR = found.pull_request
    await pr.getByRole('button', { name: 'Discover existing PR' }).click()
    await expect(pr.getByRole('button', { name: 'Create reviewed PR' })).toHaveCount(0)
    const selectedFeedback = `selected-feedback-${headBranch}`
    const omittedFeedback = `not-selected-${headBranch}`
    for (const comment of [selectedFeedback, omittedFeedback]) {
      await exec('docker', ['exec', container, 'gh', 'api', '--hostname', 'github.com', '--method', 'POST', `repos/${baseRepository}/issues/${prNumber}/comments`, '-f', `body=${comment}`])
    }
    await pr.getByRole('button', { name: 'Refresh PR feedback' }).click()
    const feedback = pr.getByRole('region', { name: 'PR feedback' })
    await feedback.locator('article').filter({ hasText: selectedFeedback }).getByRole('checkbox').check()
    await feedback.getByRole('button', { name: 'Send selected feedback to the agent' }).click()
    await expect(feedback.getByRole('status')).toContainText('Delivery:')
    const room = await admin.api.rpc<{ messages: RoomMessage[] }>('run.room.list', { workspace_id: workspaceID, run_id: run.id, limit: 100 })
    const posted = room.messages.find((entry) => entry.body.includes(selectedFeedback))!
    expect(posted.kind).toBe('steer_request')
    expect(posted.body).not.toContain(omittedFeedback)
    await page.screenshot({ path: test.info().outputPath('explicit-fork-pr-feedback.png'), fullPage: true })
    evidence.selectedFeedback = posted.body

    // Exact lookup must not associate this PR with another base or fork.
    await selectPR(headBranch, `${headBranch}-missing-base`)
    await expect(pr).toContainText('No existing PR for this exact repository, base and head.')
    await selectPR(headBranch, baseBranch, baseRepository!)
    await expect(pr).toContainText('No existing PR for this exact repository, base and head.')
    const expected = { branch: status.branch, head: status.head }
    const wrongHead = await admin.api.rpc<RunPRCreateResult>('run.pr.create', {
      run_id: run.id, expected, target: targetFor(baseBranch), title: `Refuse wrong head ${headBranch}`, body: description,
    })
    expect(wrongHead.created).toBe(false)
    expect(wrongHead.pull_request).toBeFalsy()
    expect(wrongHead.error).toContain('push the selected commit first')
    const wrongBase = await admin.api.rpc<RunPRCreateResult>('run.pr.create', {
      run_id: run.id, expected, target: { ...targetFor(headBranch), base_branch: `${headBranch}-missing-base` },
      title: `Refuse wrong base ${headBranch}`, body: description,
    })
    expect(wrongBase.created).toBe(false)
    expect(wrongBase.pull_request).toBeFalsy()
    expect(wrongBase.output.exit_code).not.toBe(0)
    expect(wrongBase.error).toContain('422')
    evidence.wrongHead = wrongHead
    evidence.wrongBase = wrongBase

    // A PR created directly by the real gh CLI must be discovered by the UI.
    const directBranch = branches[1]
    await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'push', 'origin', `HEAD:refs/heads/${directBranch}`])
    await nativeGH(['pr', 'create', '--repo', baseRepository!, '--base', baseBranch, '--head', `${headRepository!.split('/')[0]}:${directBranch}`, '--title', `Aether E2E ${directBranch}`, '--body', description, '--draft'])
    await selectPR(directBranch)
    await expect(pr.getByRole('link', { name: `Aether E2E ${directBranch}`, exact: false })).toBeVisible()
    await expect(pr.getByRole('button', { name: 'Create reviewed PR' })).toHaveCount(0)
    const direct = await admin.api.rpc<RunPRStatusResult>('run.pr.status', { run_id: run.id, expected, target: targetFor(directBranch) })
    expect(direct.error).toBeFalsy()
    expect(direct.pull_request).toMatchObject({ ...targetFor(directBranch), head_oid: status.head })
    evidence.directGHCreatedPR = direct.pull_request
    await page.screenshot({ path: test.info().outputPath('direct-gh-pr-discovered.png'), fullPage: true })

    // Fault injection loses only the successful POST response. Native gh
    // really creates the PR on GitHub; every lookup remains real native gh.
    const uncertainBranch = branches[2]
    await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'push', 'origin', `HEAD:refs/heads/${uncertainBranch}`])
    await selectPR(uncertainBranch)
    await expect(pr).toContainText('No existing PR for this exact repository, base and head.')
    const ghWrapper = path.join(aether.server.memberHome(account), '.local', 'bin', 'gh')
    aether.installAgent(account, 'gh', [
      'case " $* " in',
      `  *" repos/${baseRepository}/pulls --method POST "*)`,
      `    ${ghPath} "$@" >/dev/null || exit $?`,
      '    printf "created\\n" >> /tmp/aether-github-post-count',
      '    printf "acceptance fault: successful real GitHub POST response discarded\\n" >&2',
      '    exit 75 ;;',
      'esac',
      `exec ${ghPath} "$@"`,
    ].join('\n'))
    try {
      await createPR(uncertainBranch)
      const outcome = pr.getByRole('region', { name: 'PR creation outcome' })
      await expect(outcome).toContainText('Created: no · Reconciled: yes')
      await expect(outcome).toContainText('successful real GitHub POST response discarded')
      await expect(pr.getByRole('button', { name: 'Create reviewed PR' })).toHaveCount(0)
      await page.screenshot({ path: test.info().outputPath('real-post-response-loss-reconciled.png'), fullPage: true })
    } finally {
      rmSync(ghWrapper, { force: true })
    }
    const reconciled = await admin.api.rpc<RunPRStatusResult>('run.pr.status', { run_id: run.id, expected, target: targetFor(uncertainBranch) })
    expect(reconciled.error).toBeFalsy()
    expect(reconciled.pull_request).toMatchObject({ ...targetFor(uncertainBranch), head_oid: status.head })
    const posts = await exec('docker', ['exec', container, 'cat', '/tmp/aether-github-post-count'])
    expect(posts.stdout).toBe('created\n')
    const exactPRs = await nativeGH(['api', `repos/${baseRepository}/pulls?state=all&head=${encodeURIComponent(`${headRepository!.split('/')[0]}:${uncertainBranch}`)}&base=${encodeURIComponent(baseBranch)}`])
    expect(JSON.parse(exactPRs.stdout).map((entry: { number: number }) => entry.number)).toEqual([reconciled.pull_request!.number])
    evidence.uncertainCreation = { mechanism: `Real ${ghPath} POST succeeded; member-local wrapper discarded its stdout and exited 75. Native read-only reconciliation found exactly one real PR; only one successful POST was recorded.`, pullRequest: reconciled.pull_request }

    // Independently advance a disposable remote branch using native Git.
    // The UI must expose real non-fast-forward rejection, never force it.
    const rejectedBranch = branches[3]
    const advanced = await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'commit-tree', `${status.head}^{tree}`, '-p', status.head, '-m', 'Independent disposable remote advance'])
    await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'push', 'origin', `${advanced.stdout.trim()}:refs/heads/${rejectedBranch}`])
    await page.getByLabel('Push head branch', { exact: true }).fill(rejectedBranch)
    await page.getByRole('checkbox', { name: 'I reviewed the run account, branch, HEAD and exact push destination above.' }).check()
    await page.getByRole('button', { name: 'Push reviewed branch' }).click()
    const push = page.getByRole('region', { name: 'Push outcome' })
    await expect(push).toContainText('Pushed: no')
    await expect(push).toContainText('non-fast-forward')
    const remote = await nativeGH(['api', `repos/${headRepository}/git/ref/heads/${rejectedBranch}`, '--jq', '.object.sha'])
    expect(remote.stdout.trim()).toBe(advanced.stdout.trim())
    evidence.pushRejection = { stderr: await push.innerText(), remoteHead: remote.stdout.trim(), localHead: status.head }
    await page.screenshot({ path: test.info().outputPath('real-github-push-rejection.png'), fullPage: true })

    // Remove only this disposable member's local credential. Never revoke
    // the user's token; finally uses a process-scoped token for cleanup.
    rmSync(credentialPath, { force: true })
    await page.getByRole('button', { name: 'Refresh status' }).click()
    await selectPR(headBranch)
    await expect(pr.getByRole('alert')).toBeVisible()
    await expect(pr.getByRole('button', { name: 'Create reviewed PR' })).toBeDisabled()
    const withoutCredential = await admin.api.rpc<RunGitStatusResult>('run.git.status', { run_id: run.id })
    expect(withoutCredential.identity).toBeFalsy()
    expect(withoutCredential.identity_error).toBeTruthy()
    expect(withoutCredential.head).toBe(status.head)
    evidence.removedMemberCredential = withoutCredential.identity_error
    await page.screenshot({ path: test.info().outputPath('member-credential-removed.png'), fullPage: true })

    const { workspace } = await admin.api.rpc<{ workspace: Workspace }>('workspace.get', { workspace_id: workspaceID })
    expect(workspace.origin).toBe(imported.workspace.origin)
    expect(workspace.base_branch).toBe(imported.workspace.base_branch)
    const mirror = await admin.api.rpc<WorkspaceMirrorResult>('workspace.mirror.status', { workspace_id: workspaceID })
    expect(mirror.source_url).toBe(sourceBeforePublish.source_url)
    expect(mirror.accepted_commit).toBe(sourceBeforePublish.accepted_commit)
    evidence.mirror = { sourceURL: mirror.source_url, acceptedBefore: sourceBeforePublish.accepted_commit, acceptedAfter: mirror.accepted_commit, origin: workspace.origin }
  } finally {
    // Query exact owned heads even after a lost response; never retry create.
    // Credentials are passed only through inherited env, not Docker argv.
    const cleanupGH = async (args: string[]) => exec('docker', ['exec', '-e', 'GH_TOKEN', container, ghPath, 'api', ...args], { env: { ...process.env, GH_TOKEN: token! } })
    const cleanup: { branch: string; closedPRs: number[]; deleted: boolean }[] = []
    try {
      for (const branch of branches) {
        const prs = await cleanupGH([`repos/${baseRepository}/pulls?state=open&head=${encodeURIComponent(`${headRepository!.split('/')[0]}:${branch}`)}&base=${encodeURIComponent(baseBranch)}`])
        const owned = (JSON.parse(prs.stdout) as { number: number; head: { ref: string; repo: { full_name: string } } }[]).filter((entry) => entry.head.ref === branch && entry.head.repo.full_name.toLowerCase() === headRepository!.toLowerCase())
        for (const entry of owned) await cleanupGH(['--method', 'PATCH', `repos/${baseRepository}/pulls/${entry.number}`, '-f', 'state=closed'])
        const refs = await cleanupGH([`repos/${headRepository}/git/matching-refs/heads/${branch}`])
        const exists = (JSON.parse(refs.stdout) as { ref: string }[]).some((entry) => entry.ref === `refs/heads/${branch}`)
        if (exists) await cleanupGH(['--method', 'DELETE', `repos/${headRepository}/git/refs/heads/${branch}`])
        cleanup.push({ branch, closedPRs: owned.map((entry) => entry.number), deleted: exists })
      }
    } finally {
      evidence.cleanup = cleanup
      await test.info().attach('safe-github-acceptance-evidence', { body: JSON.stringify(evidence, null, 2), contentType: 'application/json' })
    }
  }
})
