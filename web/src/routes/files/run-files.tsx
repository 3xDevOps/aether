import { useEffect, useMemo, useState } from 'react'
import { GitBranch } from '@/components/icons'
import { api, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { useElementSize } from '@/lib/hooks'
import { runLabel } from '@/lib/status'
import type { Run } from '@/lib/types'
import { FileEditor, type ViewerMode } from '@/routes/files/editor'
import { closeTab, FilesPane } from '@/routes/files/pane'
import type { FileTarget } from '@/routes/files/paths'
import { fileTabFromSource, fileTabToSelection, sourceKey, type WorkspaceSource } from '@/routes/files/sources'
import { touchedPaths, TreeDirectory, TreeExpansion } from '@/routes/files/tree'
import { isRunRoute } from '@/routes/run/views'
import { useStore } from '@/store'

/** Below this the tree and an editor do not both fit in the pane. */
const besideWidth = 640

/** One run's checkout: its tree and the editor, as a view of the run page. */
export function RunFiles({ run, shown, client = api }: { run: Run; shown: boolean; client?: Api }) {
  const route = useStore((s) => s.route)
  const filesEpoch = useStore((s) => s.filesEpoch)
  const fileTabs = useStore((s) => s.fileTabs)
  const activeFileKey = useStore((s) => s.activeFileKey)
  const openFileTab = useStore((s) => s.openFileTab)
  const mobile = useIsMobile()
  const [pane, size] = useElementSize<HTMLDivElement>()
  const [mode, setMode] = useState<ViewerMode>('file')
  const [browsing, setBrowsing] = useState(false)
  const [reveal, setReveal] = useState<{ key: string; target: FileTarget } | null>(null)

  const label = runLabel(run)
  const source = useMemo<WorkspaceSource>(
    () => ({ kind: 'workspace', workspaceID: run.workspace_id, runID: run.id, label }),
    [run.workspace_id, run.id, label],
  )
  const tabs = fileTabs.filter((tab) => tab.kind === 'workspace' && tab.runID === run.id).map(fileTabToSelection)
  const selection = tabs.find((tab) => sourceKey(tab, tab.path) === activeFileKey) ?? tabs.at(-1) ?? null

  const open = (path: string, target: FileTarget | null) => {
    const key = sourceKey(source, path)
    openFileTab(fileTabFromSource(source, path, key))
    setMode('file')
    setBrowsing(false)
    setReveal(target && { key, target })
  }

  // A link names its file, and its line, in the route. Opening the same link
  // again is a new route, so it shows the line again.
  useEffect(() => {
    const { view, file, line, column } = route.params
    if (!isRunRoute(route, run.id) || view !== 'files' || !file) return
    open(file, line ? { path: file, line: Number(line), ...(column ? { column: Number(column) } : {}) } : null)
  }, [route, run.id])

  const tree = (
    <div className="p-2">
      <TreeDirectory
        source={{ ...source, label: run.branch }}
        path=""
        depth={0}
        client={client}
        onSelect={(_, path) => open(path, null)}
        selected={selection}
        touched={touchedPaths(run.id)}
        icon={<GitBranch />}
      />
    </div>
  )
  const key = selection && sourceKey(selection, selection.path)
  const narrow = mobile || (size.width > 0 && size.width < besideWidth)
  const editor = selection && (
    <FileEditor
      selection={selection}
      tabs={tabs}
      mode={mode}
      onMode={setMode}
      onSelect={(_, path) => open(path, null)}
      onClose={(tab) => {
        if (closeTab(tab, tabs)) setMode('file')
      }}
      onBrowse={narrow ? () => setBrowsing(true) : undefined}
      reveal={reveal?.key === key ? reveal.target : null}
      shown={shown}
      client={client}
      epoch={filesEpoch}
    />
  )

  return (
    <TreeExpansion reveal={selection}>
      <div ref={pane} className="flex h-full min-h-0 min-w-0 flex-col bg-canvas">
        <FilesPane narrow={narrow} tree={tree} editor={editor} browsing={browsing} onBrowsing={setBrowsing} />
      </div>
    </TreeExpansion>
  )
}
