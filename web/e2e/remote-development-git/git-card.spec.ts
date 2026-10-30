import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { expect, test } from '../fixtures'
import { runContainer } from '../harness/docker'
import { dockerReachable } from '../harness/server'
import { memberID, seedWorkspace } from '../harness/setup'
import type { Workspace } from '../../src/lib/types'

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

test('the git card draws the run branch against a base that moved, names the conflict, and follows a new commit', async ({ page, aether }) => {
  test.setTimeout(180_000)
  const admin = await aether.member('Git card administrator')
  const repo = await aether.seedRepo('git-card')
  await seedWorkspace(admin, aether.server.addr, repo)
  aether.installAgent(await memberID(admin), 'claude', 'sleep 600')
  const { workspaces } = await admin.api.rpc<{ workspaces: Workspace[] }>('workspace.list')
  const { run } = await admin.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, harness: 'claude', task: 'git card' })
  const container = runContainer(run.id)
  const sh = (script: string[]) => exec('docker', ['exec', '-w', '/workspace', container, 'sh', '-c', script.join(' && ')])
  // Two commits on the run branch and one on origin/main, both editing README.md.
  await sh([
    'git config user.name "Ada Lovelace"',
    'git config user.email ada@example.invalid',
    'git config commit.gpgsign false',
    'git worktree add -q --detach /tmp/upstream origin/main',
    'printf "# upstream title\\n" > /tmp/upstream/README.md',
    'git -C /tmp/upstream commit -qam "Rename the project upstream"',
    'git update-ref refs/remotes/origin/main "$(git -C /tmp/upstream rev-parse HEAD)"',
    'printf "# session title\\n" > README.md',
    'git commit -qam "Retitle the README"',
    'printf "notes\\n" > notes.txt',
    'git add notes.txt',
    'git commit -qm "Add release notes"',
  ])

  await page.goto(`${admin.url}&run=${run.id}`)
  await page.getByRole('tab', { name: 'Changes', exact: true }).click()
  const card = page.getByRole('region', { name: 'Branch and pull request' })
  const commits = card.getByRole('list', { name: 'Commits' })
  await expect(commits.getByRole('listitem')).toHaveCount(5)
  await expect(commits.getByRole('listitem').nth(0)).toContainText('Open pull request')
  await expect(commits.getByRole('listitem').nth(0)).toContainText('1 conflict')
  await expect(commits).toContainText('Add release notes')
  await expect(commits).toContainText('Retitle the README')
  await expect(commits.getByRole('listitem').filter({ hasText: 'Rename the project upstream' })).toContainText('1 conflict')
  await expect(commits).toContainText('Merge base with origin/main')
  await expect(card).toContainText('2 ahead · 1 behind origin/main')
  const readiness = card.getByRole('region', { name: 'Merge readiness' })
  await expect(readiness).toContainText('1 file conflicts with origin/main as last fetched')
  await expect(readiness).toContainText('1 commit behind origin/main as last fetched')
  await expect(readiness).not.toContainText('Ready to merge')

  await card.getByRole('button', { name: '1 conflict' }).click()
  await expect(page.getByRole('dialog').getByText('README.md', { exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await page.screenshot({ path: test.info().outputPath('git-card.png'), fullPage: true })

  // A commit the agent makes reaches the card through git.branch, without a reload.
  await sh(['printf "more\\n" >> notes.txt', 'git commit -qam "Extend the release notes"'])
  await expect(commits).toContainText('Extend the release notes', { timeout: 60_000 })
  await expect(card).toContainText('3 ahead · 1 behind origin/main')
})
