import { useCallback, useEffect, useRef, useState } from 'react'
import type * as React from 'react'

/** What a drop reads when more files are dragged than there is room for. */
export function fitLabel(limit: number): string {
  return `At most ${limit} ${limit === 1 ? 'file fits' : 'files fit'}`
}

function hasFiles(transfer: DataTransfer | null): boolean {
  return Boolean(transfer && Array.from(transfer.types).includes('Files'))
}

/**
 * Drag handlers for a drop target. `count` is the number of files under the
 * pointer, null when no file drag is over the target; a text or link drag
 * never counts. Entering a child fires leave on its parent, so a depth counter
 * decides when the drag really left. Over `limit` files the cursor shows no
 * drop, and a drop adds nothing. Events stop here: React bubbles them through
 * portals, so a popover's zone must not also drive the composer around it.
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

  const handlers = {
    onDragEnter: (event: React.DragEvent) => {
      if (!hasFiles(event.dataTransfer)) return
      event.preventDefault()
      event.stopPropagation()
      depth.current += 1
      // A file dragged from a page also carries its URL and markup as string items.
      setCount(Array.from(event.dataTransfer.items).filter((item) => item.kind === 'file').length || 1)
    },
    onDragOver: (event: React.DragEvent) => {
      if (!hasFiles(event.dataTransfer)) return
      event.preventDefault()
      event.stopPropagation()
      event.dataTransfer.dropEffect = (count ?? 1) > limit ? 'none' : 'copy'
    },
    onDragLeave: (event: React.DragEvent) => {
      if (!hasFiles(event.dataTransfer)) return
      event.stopPropagation()
      depth.current = Math.max(0, depth.current - 1)
      if (depth.current === 0) setCount(null)
    },
    onDrop: (event: React.DragEvent) => {
      if (!hasFiles(event.dataTransfer)) return
      event.preventDefault()
      event.stopPropagation()
      end()
      if (limit > 0) onFiles(Array.from(event.dataTransfer.files))
    },
  }
  return { count, refused: count !== null && count > limit, handlers }
}

/** A file dropped outside a target would replace the dashboard with that file. */
export function useRefuseStrayFileDrops() {
  useEffect(() => {
    const refuse = (event: DragEvent) => {
      if (event.defaultPrevented || !event.dataTransfer?.types.includes('Files')) return
      event.preventDefault()
      event.dataTransfer.dropEffect = 'none'
    }
    window.addEventListener('dragover', refuse)
    window.addEventListener('drop', refuse)
    return () => {
      window.removeEventListener('dragover', refuse)
      window.removeEventListener('drop', refuse)
    }
  }, [])
}
