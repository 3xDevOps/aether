import { useEffect, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { FileEditor, type ViewerMode } from '@/routes/files/editor'
import { HomeFiles } from '@/routes/files/home'
import { HomeStatus, HomeTree } from '@/routes/files/home-tree'
import { closeTab, FilesPane } from '@/routes/files/pane'
import {
  type FileSource,
  fileTabFromSource,
  fileTabToSelection,
  homeSource,
  isLiveRun,
  sourceKey,
} from '@/routes/files/sources'
import { FileTree, TreeExpansion } from '@/routes/files/tree'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const homeRoot = { ...homeSource, path: '' }

export function FilesRoute({ params, client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore(useShallow((s) => Object.values(s.workspaces)))
  const liveRuns = useStore(useShallow((s) => Object.values(s.runs).filter(isLiveRun)))
  const filesEpoch = useStore((s) => s.filesEpoch)
  const identityKey = useStore((s) => s.identityKey)
  const fileTabs = useStore((s) => s.fileTabs)
  const activeFileKey = useStore((s) => s.activeFileKey)
  const openFileTab = useStore((s) => s.openFileTab)
  const setActiveFileKey = useStore((s) => s.setActiveFileKey)
  const capabilities = useCapability()
  const mobile = useIsMobile()
  const [mode, setMode] = useState<ViewerMode>('file')
  const [browsing, setBrowsing] = useState(false)

  useEffect(() => {
    setBrowsing(false)
  }, [identityKey])

  const tabs = fileTabs.map(fileTabToSelection)
  const selection = tabs.find((tab) => sourceKey(tab, tab.path) === activeFileKey) ?? null

  useEffect(() => {
    if (activeFileKey && tabs.some((tab) => sourceKey(tab, tab.path) === activeFileKey)) return
    const last = tabs.at(-1)
    const nextKey = last ? sourceKey(last, last.path) : null
    if (nextKey !== activeFileKey) setActiveFileKey(nextKey)
  }, [activeFileKey, setActiveFileKey, tabs])

  const select = (source: FileSource, path: string) => {
    openFileTab(fileTabFromSource(source, path, sourceKey(source, path)))
    setMode('file')
    setBrowsing(false)
  }

  const canBrowseWorkspaces = capabilities.hasMethod('files.tree')
  const canBrowseHome = capabilities.hasMethod('home.tree')
  if (!canBrowseWorkspaces && !canBrowseHome) return null
  const shownWorkspaces = canBrowseWorkspaces ? workspaces : []

  const tree = (
    <div className="flex min-h-full flex-col">
      <FileTree
        workspaces={shownWorkspaces}
        runs={canBrowseWorkspaces ? liveRuns : []}
        home={canBrowseHome && <HomeTree onSelect={select} selected={selection} />}
        client={client}
        onSelect={select}
        selected={selection}
      />
      {shownWorkspaces.length === 0 && !canBrowseHome && <p className="px-4 py-3 text-ui text-muted">No workspaces to browse.</p>}
      {canBrowseHome && <HomeStatus />}
    </div>
  )

  const editor = selection && (
    <FileEditor
      selection={selection}
      tabs={tabs}
      mode={mode}
      onMode={setMode}
      onSelect={select}
      onClose={(tab) => {
        if (closeTab(tab, tabs)) setMode('file')
      }}
      onBrowse={mobile ? () => setBrowsing(true) : undefined}
      client={client}
      epoch={filesEpoch}
    />
  )

  const view = (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title="Files" />
      <FilesPane narrow={mobile} tree={tree} editor={editor} browsing={browsing} onBrowsing={setBrowsing} />
    </div>
  )

  return (
    <TreeExpansion reveal={params.home ? homeRoot : null}>
      {canBrowseHome ? (
        <HomeFiles key={identityKey} client={client} onOpen={(path) => select(homeSource, path)}>
          {view}
        </HomeFiles>
      ) : (
        view
      )}
    </TreeExpansion>
  )
}

registerRoute('files', FilesRoute)
