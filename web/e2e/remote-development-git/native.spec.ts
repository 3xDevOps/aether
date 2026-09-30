import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { expect, test } from '../fixtures'
import { runContainer } from '../harness/docker'
import { dockerReachable } from '../harness/server'
import { memberID, seedWorkspace } from '../harness/setup'
import type { RunGitStatusResult, Workspace } from '../../src/lib/types'

const exec = promisify(execFile)

const nativeImage = process.env.AETHER_E2E_STANDARD_IMAGE ?? 'aether-standard:ci'
test.use({ serverOptions: { standardImage: nativeImage } })

test.beforeAll(async () => {
  if (!dockerReachable()) throw new Error('Native Git acceptance requires a reachable Docker daemon; start Docker before running this suite.')
  try {
    await exec('docker', ['image', 'inspect', nativeImage])
  } catch {
    throw new Error(`Required native Git image ${nativeImage} is missing. Build it from the repository root: docker build -f images/standard/Dockerfile -t ${nativeImage} .`)
  }
})

test('selected paths preserve unrelated staging, report index failure, and retain a real push when GitHub is unavailable', async ({ page, aether }) => {
  test.setTimeout(180_000)
  const admin = await aether.member('Native Git administrator')
  const repo = await aether.seedRepo('native-git')
  await seedWorkspace(admin, aether.server.addr, repo)
  aether.installAgent(await memberID(admin), 'claude', 'sleep 600')
  const { workspaces } = await admin.api.rpc<{ workspaces: Workspace[] }>('workspace.list')
  const workspace = workspaces[0]
  const { run } = await admin.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspace.id, harness: 'claude', task: 'native selected Git transaction' })
  await page.goto(`${admin.url}&run=${run.id}`)
  const container = runContainer(run.id)
  await exec('docker', ['exec', '-w', '/workspace', container, 'sh', '-c', [
    'git config user.name "Native E2E"',
    'git config user.email native-e2e@example.invalid',
    'git config commit.gpgsign false',
    'printf "selected first\\n" > selected.txt',
    'printf "unrelated staged\\n" > unrelated.txt',
    'git add unrelated.txt',
    'git init --bare /tmp/published.git',
    'git remote add writable /tmp/published.git',
  ].join(' && ')])
  await page.getByRole('tab', { name: 'Diff', exact: true }).click()
  await page.getByText('Native changes & publish', { exact: true }).click()
  await page.getByRole('checkbox', { name: 'Select selected.txt', exact: true }).check()
  await page.getByRole('button', { name: 'Review selected paths' }).click()
  await expect(page.getByRole('region', { name: 'Untracked contents: selected.txt' })).toContainText('selected first')
  await page.getByLabel('Commit message', { exact: true }).fill('Commit only selected path')
  await page.getByRole('button', { name: 'Commit selected paths' }).click()
  const commit = page.getByRole('region', { name: 'Commit outcome' })
  await expect(commit).toContainText('Committed: yes')
  await expect(commit).toContainText('Index updated: yes')
  const committed = await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'show', 'HEAD:selected.txt'])
  expect(committed.stdout).toBe('selected first\n')
  const staged = await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'diff', '--cached', '--name-only'])
  expect(staged.stdout.trim()).toBe('unrelated.txt')

  // A real native index lock creates a partial outcome after the isolated
  // selected-path commit has published its ref. No response is mocked.
  await exec('docker', ['exec', '-w', '/workspace', container, 'sh', '-c', 'printf "selected second\\n" > selected.txt && touch .git/index.lock'])
  try {
    await page.getByRole('button', { name: 'Refresh native status' }).click()
    await page.getByRole('checkbox', { name: 'Select selected.txt', exact: true }).check()
    await page.getByRole('button', { name: 'Review selected paths' }).click()
    await page.getByLabel('Commit message', { exact: true }).fill('Publish despite unrelated native index lock')
    await page.getByRole('button', { name: 'Commit selected paths' }).click()
    await expect(commit).toContainText('Committed: yes')
    await expect(commit).toContainText('Index updated: no')
    await expect(commit).toContainText('index.lock')
    const published = await exec('docker', ['exec', '-w', '/workspace', container, 'git', 'show', 'HEAD:selected.txt'])
    expect(published.stdout).toBe('selected second\n')
  } finally {
    await exec('docker', ['exec', '-w', '/workspace', container, 'rm', '-f', '.git/index.lock'])
  }

  await page.getByRole('combobox', { name: 'Push remote', exact: true }).selectOption('writable')
  await page.getByRole('combobox', { name: 'Writable push URL', exact: true }).selectOption('/tmp/published.git')
  await page.getByLabel('Push head branch', { exact: true }).fill('reviewed-native')
  await page.getByRole('checkbox', { name: 'I reviewed the run account, branch, HEAD and exact push destination above.' }).check()
  await page.getByRole('button', { name: 'Push reviewed branch' }).click()
  const push = page.getByRole('region', { name: 'Push outcome' })
  await expect(push).toContainText('Pushed: yes')
  const remoteHead = await exec('docker', ['exec', container, 'git', '--git-dir=/tmp/published.git', 'rev-parse', 'refs/heads/reviewed-native'])
  const status = await admin.api.rpc<RunGitStatusResult>('run.git.status', { run_id: run.id })
  expect(remoteHead.stdout.trim()).toBe(status.head)

  // No GitHub credentials are installed by this scenario. The actual native
  // gh failure is visible and cannot erase the successful native push.
  const pr = page.getByRole('region', { name: 'GitHub pull request' })
  await pr.getByLabel('PR repository (owner/name)', { exact: true }).fill('upstream/repository')
  await pr.getByLabel('PR base branch', { exact: true }).fill('main')
  await pr.getByLabel('PR head repository (owner/name)', { exact: true }).fill('fork-owner/repository')
  await pr.getByLabel('PR head branch', { exact: true }).fill('reviewed-native')
  await pr.getByRole('button', { name: 'Discover existing PR' }).click()
  await expect(pr.getByRole('alert')).toBeVisible()
  await expect(pr.getByRole('button', { name: 'Create reviewed PR' })).toBeDisabled()
  await expect(push).toContainText('Pushed: yes')
  const { workspace: unchanged } = await admin.api.rpc<{ workspace: Workspace }>('workspace.get', { workspace_id: workspace.id })
  expect(unchanged.origin ?? '').toBe(workspace.origin ?? '')
  expect(unchanged.base_branch).toBe(workspace.base_branch)
  await page.screenshot({ path: test.info().outputPath('native-push-pr-error.png'), fullPage: true })
  // An independently advanced remote must reject a non-fast-forward without
  // changing the reviewed local branch or forcing the remote backwards.
  const remoteCommit = await exec('docker', ['exec', container, 'git', '--git-dir=/tmp/published.git', '-c', 'user.name=Remote E2E', '-c', 'user.email=remote-e2e@example.invalid', 'commit-tree', `${status.head}^{tree}`, '-p', status.head, '-m', 'independent remote advance'])
  await exec('docker', ['exec', container, 'git', '--git-dir=/tmp/published.git', 'update-ref', 'refs/heads/reviewed-native', remoteCommit.stdout.trim(), status.head])
  await page.getByRole('checkbox', { name: 'I reviewed the run account, branch, HEAD and exact push destination above.' }).check()
  await page.getByRole('button', { name: 'Push reviewed branch' }).click()
  await expect(push).toContainText('Pushed: no')
  await expect(push.getByRole('alert')).toBeVisible()
  const stillRemote = await exec('docker', ['exec', container, 'git', '--git-dir=/tmp/published.git', 'rev-parse', 'refs/heads/reviewed-native'])
  expect(stillRemote.stdout.trim()).toBe(remoteCommit.stdout.trim())
  const unchangedLocal = await admin.api.rpc<RunGitStatusResult>('run.git.status', { run_id: run.id })
  expect(unchangedLocal.head).toBe(status.head)
})
