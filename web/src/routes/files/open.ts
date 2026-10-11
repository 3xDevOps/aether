import { api, type Api } from '@/lib/api'
import type { FileTreeEntry, Run } from '@/lib/types'
import { findPaths, type FileTarget, type PathMatch } from '@/routes/files/paths'
import { isLiveRun } from '@/routes/files/sources'
import { useStore } from '@/store'
import { filesKey, type FileTreeState } from '@/store/files'
import { capability, type Capability } from '@/store/hooks'

type Checkout = Pick<Run, 'id' | 'workspace_id'>

/** A run that still has its checkout, on a gateway that serves it, has a Files view. */
export function hasFiles(run: Run, cap: Capability): boolean {
  return cap.hasMethod('files.tree') && isLiveRun(run)
}

export function openInFiles(runID: string, target: FileTarget) {
  useStore.getState().navigate('run', {
    runId: runID,
    view: 'files',
    file: target.path,
    ...(target.line === undefined ? {} : { line: String(target.line) }),
    ...(target.column === undefined ? {} : { column: String(target.column) }),
  })
}

function listed(trees: Record<string, FileTreeState>, run: Checkout, dir: string): FileTreeEntry[] | undefined {
  const tree = trees[filesKey(run.workspace_id, run.id, dir)]
  return tree && !tree.loading && !tree.error ? tree.entries : undefined
}

function split(path: string): { dirs: string[]; name: string } {
  const dirs = path.split('/')
  return { name: dirs.pop()!, dirs }
}

/** Whether the directory listings already in the store say path is a file. */
export function isListedFile(trees: Record<string, FileTreeState>, run: Checkout, path: string): boolean {
  const { dirs, name } = split(path)
  return listed(trees, run, dirs.join('/'))?.some((entry) => entry.kind === 'file' && entry.name === name) ?? false
}

const requests = new Map<string, Promise<FileTreeEntry[]>>()

function listing(run: Checkout, dir: string, client: Api): Promise<FileTreeEntry[]> {
  const state = useStore.getState()
  const known = listed(state.trees, run, dir)
  if (known) return Promise.resolve(known)
  const key = filesKey(run.workspace_id, run.id, dir)
  let request = requests.get(key)
  if (!request) {
    const epoch = state.filesEpoch
    request = client
      .filesTree({ workspace_id: run.workspace_id, run_id: run.id, path: dir })
      .then(({ entries }) => {
        const now = useStore.getState()
        if (now.filesEpoch === epoch) now.setTree(key, { entries, loading: false, error: undefined })
        return entries
      })
      .finally(() => requests.delete(key))
    requests.set(key, request)
  }
  return request
}

/** Whether path is a file in the run's checkout. It reads the listings the
 * store has and fetches at most two: the root, which rules out prose such as
 * "and/or" without a request of its own, and the file's directory. */
export async function isRunFile(run: Checkout, path: string, client: Api = api): Promise<boolean> {
  const { dirs, name } = split(path)
  if (dirs.length > 0) {
    const root = await listing(run, '', client)
    if (!root.some((entry) => entry.kind === 'dir' && entry.name === dirs[0])) return false
  }
  const entries = await listing(run, dirs.join('/'), client)
  return entries.some((entry) => entry.kind === 'file' && entry.name === name)
}

/** The parts of one line of a run's terminal output that name a file in its checkout. */
export async function findRunFiles(runID: string, text: string): Promise<PathMatch[]> {
  const state = useStore.getState()
  const run = state.runs[runID]
  if (!run || !hasFiles(run, capability(state.capabilities))) return []
  const matches = findPaths(text)
  const files = await Promise.all(matches.map((match) => isRunFile(run, match.path).catch(() => false)))
  return matches.filter((_, index) => files[index])
}
