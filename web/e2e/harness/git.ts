// Real git repositories on disk, the ones the Repository step points the
// gateway at. Nothing here talks to the server: the wizard does that.

import { execFile } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { promisify } from 'node:util'

const run = promisify(execFile)

/**
 * The deterministic agent the `fake` harness runs, committed to the seed
 * repository the way the Go integration suite commits it. The leading sleep
 * keeps its first output behind the supervisor's attach point.
 */
const agentScript = `sleep 1
echo agent-ready
printf 'hello-from-agent\\n' > result.txt
`

/** git with a fixed identity, so a fresh HOME needs no gitconfig. */
async function git(repo: string, ...args: string[]): Promise<string> {
  const { stdout } = await run(
    'git',
    [
      '-C',
      repo,
      '-c',
      'user.name=E2E',
      '-c',
      'user.email=e2e@example.invalid',
      '-c',
      'commit.gpgsign=false',
      ...args,
    ],
    { timeout: 60_000 },
  )
  return stdout
}

/** Creates a repository with one commit on `main` and returns its path. */
export async function seedRepo(dir: string, name: string): Promise<string> {
  const repo = path.join(dir, name)
  mkdirSync(repo, { recursive: true })
  await git(repo, 'init', '-q', '-b', 'main')
  writeFileSync(path.join(repo, 'README.md'), `# ${name}\n`)
  writeFileSync(path.join(repo, 'agent.sh'), agentScript)
  await git(repo, 'add', '-A')
  await git(repo, 'commit', '-q', '-m', 'seed')
  return repo
}

/** Clones a local repository, as a second member joining a project would. */
export async function cloneRepo(
  dir: string,
  source: string,
  name: string,
): Promise<string> {
  const repo = path.join(dir, name)
  mkdirSync(dir, { recursive: true })
  await run('git', ['clone', '-q', source, repo], { timeout: 60_000 })
  return repo
}
