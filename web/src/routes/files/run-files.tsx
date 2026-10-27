import { useEffect, useMemo, useState } from 'react'
import { GitBranch } from '@/components/icons'
import { api, type Api } from '@/lib/api'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useIsMobile } from '@/lib/breakpoints'
import { useElementSize } from '@/lib/hooks'
import { runLabel } from '@/lib/status'
import type { Run } from '@/lib/types'
import { useRepoPatch } from '@/routes/diff/repo-patches'
import { useRepoRuns } from '@/routes/diff/repos'
import { FileEditor, type ViewerMode } from '@/routes/files/editor'
import { closeTab, FilesPane } from '@/routes/files/pane'
import type { FileTarget } from '@/routes/files/paths'
import { fileTabFromSource, fileTabToSelection, sourceKey, type FileSource, type WorkspaceSource } from '@/routes/files/sources'
import { RunCheckoutTree, touchedPaths, TreeExpansion } from '@/routes/files/tree'
import { isRunRoute } from '@/routes/run/views'
import { useStore } from '@/store'

/** Below this the tree and an editor do not both fit in the pane. */
const besideWidth = 640

/** Each run's open file, since activeFileKey is shared by every run's tabs. */
const lastSelected = new Map<string, string>()

export function RunFiles({ run, shown, client = api }: { run: Run; shown: boolean; client?: Api }) {
  const route = useStore((s) => s.route)
  const filesEpoch = useStore((s) => s.filesEpoch)
  const fileTabs = useStore((s) => s.fileTabs)
  const activeFileKey = useStore((s) => s.activeFileKey)
  const openFileTab = useStore((s) => s.openFileTab)
  const setActiveFileKey = useStore((s) => s.setActiveFileKey)
  const mobile = useIsMobile()
  const [pane, size] = useElementSize<HTMLDivElement>()
  const [mode, setMode] = useState<ViewerMode>('file')
  const [browsing, setBrowsing] = useState(false)
  const [reveal, setReveal] = useState<{ key: string; target: FileTarget } | null>(null)

  const repos = useRepoRuns(run)
  const [repoID, setRepoID] = useState('')
  const chosen = repos.find((entry) => entry.repo.workspace_id === repoID && !entry.repo.primary)?.repo
  const repoPatch = useRepoPatch(run.id, chosen?.workspace_id ?? '', Boolean(chosen))
  const repoTouched = useMemo(() => new Set(repoPatch.files.map((file) => file.path)), [repoPatch.files])
  const label = chosen ? `${chosen.name} · ${runLabel(run)}` : runLabel(run)
  const source = useMemo<WorkspaceSource>(
    () => ({ kind: 'workspace', workspaceID: run.workspace_id, runID: run.id, ...(chosen ? { repo: chosen.workspace_id } : {}), label }),
    [run.workspace_id, run.id, chosen?.workspace_id, label],
  )
  const tabs = fileTabs.filter((tab) => tab.kind === 'workspace' && tab.runID === run.id).map(fileTabToSelection)
  const holds = (key: string | null | undefined) => tabs.find((tab) => sourceKey(tab, tab.path) === key)
  const selection = holds(activeFileKey) ?? holds(lastSelected.get(run.id)) ?? tabs.at(-1) ?? null

  const open = (path: string, target: FileTarget | null, from: FileSource = source) => {
    const key = sourceKey(from, path)
    openFileTab(fileTabFromSource(from, path, key))
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

  const selectTab = (tab: FileSource, path: string) => {
    if (tab.kind !== 'workspace') return
    setRepoID(tab.repo ?? '')
    open(path, null, tab)
  }

  const tree = (
    <div className="p-2">
      {repos.length > 0 && (
        <Tabs value={chosen?.workspace_id ?? ''} onValueChange={setRepoID} className="mb-2">
          <TabsList look="segmented" aria-label="Repos" className="overflow-x-auto [scrollbar-width:none]">
            {repos.map(({ repo }) => (
              <TabsTrigger key={repo.workspace_id} value={repo.primary ? '' : repo.workspace_id}>{repo.name}</TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      )}
      <RunCheckoutTree
        source={{ ...source, label: chosen?.branch ?? run.branch }}
        depth={0}
        client={client}
        onSelect={(_, path) => open(path, null)}
        selected={selection}
        touched={chosen ? repoTouched : touchedPaths(run.id)}
        icon={<GitBranch />}
      />
    </div>
  )
  const key = selection && sourceKey(selection, selection.path)
  useEffect(() => {
    if (!key) return
    lastSelected.set(run.id, key)
    const { activeFileKey: current, fileTabs: open } = useStore.getState()
    if (!open.some((tab) => tab.kind === 'workspace' && tab.runID === run.id && tab.key === current)) setActiveFileKey(key)
  }, [key, run.id, setActiveFileKey])
  const narrow = mobile || (size.width > 0 && size.width < besideWidth)
  const editor = selection && (
    <FileEditor
      selection={selection}
      tabs={tabs}
      mode={mode}
      onMode={setMode}
      onSelect={selectTab}
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
