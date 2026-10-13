import type { ReactNode } from 'react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { type Selection, sourceKey } from '@/routes/files/sources'
import { useStore } from '@/store'

/** The tree beside the editor. A narrow pane shows one at a time: the tree
 * until a file is open, then the editor, with the tree in a side sheet. */
export function FilesPane({
  narrow,
  tree,
  editor,
  browsing,
  onBrowsing,
}: {
  narrow: boolean
  tree: ReactNode
  editor: ReactNode
  browsing: boolean
  onBrowsing: (open: boolean) => void
}) {
  if (!narrow) {
    return (
      <div className="flex min-h-0 min-w-0 flex-1">
        <aside aria-label="Files" className="w-[280px] shrink-0 overflow-y-auto border-r border-seam bg-chrome">{tree}</aside>
        {editor ?? (
          <div className="flex min-w-0 flex-1 items-center justify-center">
            <EmptyState title="No file open">Pick a file in the tree to read or edit it.</EmptyState>
          </div>
        )}
      </div>
    )
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {editor ?? <aside aria-label="Files" className="min-h-0 flex-1 overflow-y-auto">{tree}</aside>}
      <Dialog open={browsing && Boolean(editor)} onOpenChange={onBrowsing}>
        <DialogContent
          variant="side"
          side="left"
          aria-describedby={undefined}
          className="w-[min(320px,85vw)]"
          // Escape in the tree's name field cancels the name, not the sheet.
          onEscapeKeyDown={(event) => {
            if (event.target instanceof HTMLInputElement) event.preventDefault()
          }}
        >
          <div className="flex h-11 shrink-0 items-center border-b border-seam px-4">
            <DialogTitle>Files</DialogTitle>
          </div>
          <aside aria-label="Files" className="min-h-0 flex-1 overflow-y-auto">{tree}</aside>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** Closes an open file, asking first when it has unsaved edits, and moves to
 * its neighbour among the tabs the view shows. True when the shown file changed. */
export function closeTab(tab: Selection, shown: Selection[]): boolean {
  const key = sourceKey(tab, tab.path)
  const state = useStore.getState()
  const draft = state.drafts[key]
  const dirty = Boolean(draft && (draft.content !== draft.baseContent || draft.saving))
  if (dirty && !window.confirm(`Discard unsaved changes to ${tab.path}?`)) return false
  if (dirty) state.clearDraft(key)
  const keys = shown.map((item) => sourceKey(item, item.path))
  const index = keys.indexOf(key)
  const wasActive = state.activeFileKey === key
  state.closeFileTab(key)
  if (!wasActive) return false
  const remaining = keys.filter((item) => item !== key)
  state.setActiveFileKey(remaining[Math.min(index, remaining.length - 1)] ?? null)
  return true
}
