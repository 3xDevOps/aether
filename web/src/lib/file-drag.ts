import { useCallback, useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { desktopBridge } from '@/components/shell/window-bar'

/** What a drop reads when more files are dragged than there is room for. */
export function fitLabel(limit: number): string {
  return `At most ${limit} ${limit === 1 ? 'file fits' : 'files fit'}`
}

function isLinuxDesktop(): boolean {
  return desktopBridge()?.platform === 'linux'
}

function hasFiles(transfer: DataTransfer | null, linuxDesktop: boolean): boolean {
  if (!transfer) return false
  if (Array.from(transfer.types).includes('Files')) return true
  return linuxDesktop && (
    (transfer.files?.length ?? 0) > 0 ||
    Array.from(transfer.items ?? []).some((item) => item.kind === 'file')
  )
}

function droppedFiles(transfer: DataTransfer, linuxDesktop: boolean): File[] {
  if (!linuxDesktop) return Array.from(transfer.files)
  const files = Array.from(transfer.files ?? [])
  return files.length ? files : Array.from(transfer.items ?? [])
    .filter((item) => item.kind === 'file')
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null)
}

/**
 * Drag handlers for a drop target. `count` is the number of files under the
 * pointer, null when no file drag is over the target; a text or link drag
 * never counts. Entering a child fires leave on its parent, so a depth counter
 * decides when the drag really left. Over `limit` files the cursor shows no
 * drop, and a drop adds nothing. Events stop here: React bubbles them through
 * portals, so a popover's zone must not also drive the composer around it. A
 * drag over a dialog or popover rendered outside the target is not the target's.
 */
export function useFileDrop({ onFiles, limit }: { onFiles: (files: File[]) => void; limit: number }) {
  const [count, setCount] = useState<number | null>(null)
  const depth = useRef(0)
  const end = useCallback(() => {
    depth.current = 0
    setCount(null)
  }, [])
  const dragging = count !== null
  // A drag that ends away from the target, or inside another window, sends no leave.
  useEffect(() => {
    if (!dragging) return
    window.addEventListener('drop', end)
    window.addEventListener('dragend', end)
    return () => {
      window.removeEventListener('drop', end)
      window.removeEventListener('dragend', end)
    }
  }, [dragging, end])

  const ours = (event: React.DragEvent) => hasFiles(event.dataTransfer, isLinuxDesktop()) && event.currentTarget.contains(event.target as Node)
  const handlers = {
    onDragEnter: (event: React.DragEvent) => {
      if (!ours(event)) return
      event.preventDefault()
      event.stopPropagation()
      depth.current += 1
      // A file dragged from a page also carries its URL and markup as string items.
      const items = Array.from(event.dataTransfer.items ?? []).filter((item) => item.kind === 'file').length
      setCount(items || (isLinuxDesktop() ? event.dataTransfer.files?.length ?? 0 : 0) || 1)
    },
    onDragOver: (event: React.DragEvent) => {
      if (!ours(event)) return
      event.preventDefault()
      event.stopPropagation()
      event.dataTransfer.dropEffect = (count ?? 1) > limit ? 'none' : 'copy'
    },
    onDragLeave: (event: React.DragEvent) => {
      if (!ours(event)) return
      event.stopPropagation()
      depth.current = Math.max(0, depth.current - 1)
      if (depth.current === 0) setCount(null)
    },
    onDrop: (event: React.DragEvent) => {
      if (!ours(event)) return
      event.preventDefault()
      event.stopPropagation()
      end()
      const linuxDesktop = isLinuxDesktop()
      const files = droppedFiles(event.dataTransfer, linuxDesktop)
      if (limit > 0 && (!linuxDesktop || files.length <= limit)) onFiles(files)
    },
  }
  return { count, refused: count !== null && count > limit, handlers }
}

/** A file dropped outside a target would replace the dashboard with that file. */
export function useRefuseStrayFileDrops() {
  useEffect(() => {
    const refuse = (event: DragEvent) => {
      const transfer = event.dataTransfer
      if (event.defaultPrevented || !transfer || !hasFiles(transfer, isLinuxDesktop())) return
      event.preventDefault()
      transfer.dropEffect = 'none'
    }
    window.addEventListener('dragover', refuse)
    window.addEventListener('drop', refuse)
    return () => {
      window.removeEventListener('dragover', refuse)
      window.removeEventListener('drop', refuse)
    }
  }, [])
}
