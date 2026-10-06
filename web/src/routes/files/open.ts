import { runLabel } from '@/lib/status'
import type { Run } from '@/lib/types'
import { useStore } from '@/store'
import { filesKey } from '@/store/files'

export function openInFiles(run: Run, path: string) {
  const store = useStore.getState()
  store.openFileTab({
    key: filesKey(run.workspace_id, run.id, path),
    kind: 'workspace',
    workspaceID: run.workspace_id,
    runID: run.id,
    path,
    label: runLabel(run),
  })
  store.navigate('files')
}
