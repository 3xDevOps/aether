import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { VList, type VListHandle } from 'virtua'
import { ArrowLeft } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { flattenWorkflow, isOpen, type WorkflowRow } from '@/components/workflow/flatten'
import { WorkflowRowItem } from '@/components/workflow/workflow-row'
import { cn } from '@/lib/utils'
import { workflowLabel, type WorkflowNode, type WorkflowState } from '@/store/workflow'

/** Past this many visible rows the tree renders only the rows in view. */
export const virtualizeAbove = 200

function find(node: WorkflowNode, id: string): WorkflowNode | undefined {
  if (node.id === id) return node
  for (const child of node.children) {
    const found = find(child, id)
    if (found) return found
  }
  return undefined
}

function states(node: WorkflowNode, into = new Map<string, { state: WorkflowState; label: string }>()) {
  into.set(node.id, { state: node.state, label: node.label })
  for (const child of node.children) states(child, into)
  return into
}

const announced = new Set<WorkflowState>(['needs-you', 'failed', 'ready', 'completed'])
const announceDelay = 1500

/** One polite region that reads out only the transitions worth hearing, gathered over a short quiet window. */
function useAnnouncement(root: WorkflowNode): string {
  const [text, setText] = useState('')
  const before = useRef<Map<string, { state: WorkflowState; label: string }> | null>(null)
  const queue = useRef<string[]>([])
  useEffect(() => {
    const now = states(root)
    const previous = before.current
    before.current = now
    if (!previous) return
    for (const [id, { state, label }] of now) {
      const was = previous.get(id)
      if (was && was.state !== state && announced.has(state)) queue.current.push(`${label}: ${workflowLabel[state]}`)
    }
    if (queue.current.length === 0) return
    const timer = window.setTimeout(() => {
      setText(queue.current.slice(-3).join('. '))
      queue.current = []
    }, announceDelay)
    return () => clearTimeout(timer)
  }, [root])
  return text
}

export function WorkflowTree({
  root,
  label,
  onSelect,
  extra,
  className,
}: {
  root: WorkflowNode
  label: string
  /** Returns true when it handled the row; otherwise selecting a parent folds or unfolds it. */
  onSelect?: (node: WorkflowNode) => boolean
  /** Inline content under a row, such as its actions; it must not repeat the row. */
  extra?: (node: WorkflowNode, expanded: boolean) => React.ReactNode
  className?: string
}) {
  const [overrides, setOverrides] = useState<ReadonlyMap<string, boolean>>(() => new Map())
  const [focusID, setFocusID] = useState<string | null>(null)
  const shown = (focusID && find(root, focusID)) || root
  const rows = useMemo(() => flattenWorkflow(shown, overrides), [shown, overrides])
  const [activeKey, setActiveKey] = useState<string | null>(null)
  const active = rows.some((row) => row.key === activeKey) ? activeKey : rows[0]?.key
  const elements = useRef(new Map<string, HTMLElement>())
  const pending = useRef<string | null>(null)
  const list = useRef<VListHandle>(null)
  const seen = useRef<Set<string> | null>(null)
  seen.current ??= new Set(rows.map((row) => row.key))
  const announcement = useAnnouncement(root)
  const virtual = rows.length > virtualizeAbove

  const latest = useRef({ rows, overrides, onSelect })
  latest.current = { rows, overrides, onSelect }

  const register = useCallback((key: string, element: HTMLElement | null) => {
    if (element) elements.current.set(key, element)
    else elements.current.delete(key)
  }, [])

  useEffect(() => {
    const key = pending.current
    if (!key) return
    const element = elements.current.get(key)
    if (element) {
      element.focus()
      pending.current = null
    }
  })

  useEffect(() => {
    for (const row of rows) seen.current!.add(row.key)
  }, [rows])

  const focusRow = useCallback((index: number) => {
    const row = latest.current.rows[index]
    if (!row) return
    pending.current = row.key
    setActiveKey(row.key)
    if (virtual) list.current?.scrollToIndex(index, { align: 'nearest' })
    elements.current.get(row.key)?.focus()
  }, [virtual])

  const setOpen = useCallback((node: WorkflowNode, open: boolean) => {
    setOverrides((current) => new Map(current).set(node.id, open))
  }, [])

  const onToggle = useCallback((row: WorkflowRow) => {
    setActiveKey(row.key)
    if (row.expanded !== undefined) setOpen(row.node, !row.expanded)
  }, [setOpen])

  const onActivate = useCallback((row: WorkflowRow) => {
    setActiveKey(row.key)
    if (row.nested !== undefined) {
      setFocusID(row.node.id)
      pending.current = row.node.id
      setActiveKey(row.node.id)
      return
    }
    if (latest.current.onSelect?.(row.node)) return
    if (row.expanded !== undefined) setOpen(row.node, !row.expanded)
  }, [setOpen])

  const onKeyDown = (event: React.KeyboardEvent) => {
    const target = event.target as HTMLElement
    if (target.getAttribute('role') !== 'treeitem') return
    const index = rows.findIndex((row) => row.key === target.dataset.key)
    const row = rows[index]
    if (!row) return
    const parent = () => rows.findIndex((item) => item.key === row.parentKey)
    switch (event.key) {
      case 'ArrowDown':
        focusRow(index + 1)
        break
      case 'ArrowUp':
        focusRow(index - 1)
        break
      case 'Home':
        focusRow(0)
        break
      case 'End':
        focusRow(rows.length - 1)
        break
      case 'ArrowRight':
        if (row.expanded === false) setOpen(row.node, true)
        else if (row.expanded) focusRow(index + 1)
        break
      case 'ArrowLeft':
        if (row.expanded) setOpen(row.node, false)
        else if (row.parentKey) focusRow(parent())
        break
      case 'Enter':
      case ' ':
        onActivate(row)
        break
      default:
        return
    }
    event.preventDefault()
  }

  const item = (row: WorkflowRow) => (
    <WorkflowRowItem
      key={row.key}
      row={row}
      active={row.key === active}
      fresh={!seen.current!.has(row.key)}
      onActivate={onActivate}
      onToggle={onToggle}
      register={register}
      extra={extra}
    />
  )

  return (
    <div className={cn('flex min-h-0 min-w-0 flex-col', className)}>
      {shown !== root && (
        <Button
          variant="ghost"
          size="sm"
          className="self-start"
          onClick={() => {
            pending.current = shown.id
            setActiveKey(shown.id)
            setFocusID(null)
            setOverrides((current) => (isOpen(shown, current) ? current : new Map(current).set(shown.id, true)))
          }}
        >
          <ArrowLeft />
          Show the whole workflow
        </Button>
      )}
      <div role="tree" aria-label={label} onKeyDown={onKeyDown} className={cn('min-w-0', virtual && 'h-[min(70vh,40rem)]')}>
        {virtual ? (
          <VList ref={list} style={{ height: '100%' }}>
            {rows.map(item)}
          </VList>
        ) : (
          rows.map(item)
        )}
      </div>
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">{announcement}</span>
    </div>
  )
}
