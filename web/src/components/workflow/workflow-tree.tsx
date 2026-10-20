import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
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

function pathTo(node: WorkflowNode, id: string): string[] | undefined {
  if (node.id === id) return [node.id]
  for (const child of node.children) {
    const path = pathTo(child, id)
    if (path) return [node.id, ...path]
  }
  return undefined
}

function states(node: WorkflowNode, into = new Map<string, { state: WorkflowState; label: string }>()) {
  into.set(node.id, { state: node.state, label: node.label })
  for (const child of node.children) states(child, into)
  return into
}

const announced = new Set<WorkflowState>(['needs-you', 'failed', 'ready', 'completed'])
const urgent = new Set<WorkflowState>(['needs-you', 'failed'])
const announceDelay = 1500
const announceAtMost = 3

/** A live stream changes the tree constantly, so the window starts at the first transition and is never extended. */
function useAnnouncement(root: WorkflowNode): string {
  const [text, setText] = useState('')
  const before = useRef<Map<string, { state: WorkflowState; label: string }> | null>(null)
  const queue = useRef<{ text: string; urgent: boolean }[]>([])
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  useEffect(() => {
    const now = states(root)
    const previous = before.current
    before.current = now
    if (!previous) return
    for (const [id, { state, label }] of now) {
      const was = previous.get(id)
      if (was && was.state !== state && announced.has(state)) queue.current.push({ text: `${label}: ${workflowLabel[state]}`, urgent: urgent.has(state) })
    }
    if (queue.current.length === 0 || timer.current !== undefined) return
    timer.current = window.setTimeout(() => {
      timer.current = undefined
      const ordered = [...queue.current.filter((item) => item.urgent), ...queue.current.filter((item) => !item.urgent)]
      setText(ordered.slice(0, announceAtMost).map((item) => item.text).join('. '))
      queue.current = []
    }, announceDelay)
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
  extra?: (node: WorkflowNode, expanded: boolean | undefined) => React.ReactNode
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
  const activeIndex = rows.findIndex((row) => row.key === active)

  const latest = useRef({ rows, overrides, onSelect })
  latest.current = { rows, overrides, onSelect }

  const container = useRef<HTMLDivElement>(null)
  // A pending focus is only applied while focus is still in the tree or nowhere, never taken from elsewhere.
  const focusPending = useCallback((key: string, element: HTMLElement) => {
    if (pending.current !== key) return
    pending.current = null
    const current = document.activeElement
    if (!current || current === document.body || container.current?.contains(current)) element.focus()
  }, [])

  const register = useCallback((key: string, element: HTMLElement | null) => {
    if (!element) {
      elements.current.delete(key)
      return
    }
    elements.current.set(key, element)
    focusPending(key, element)
  }, [focusPending])

  // A row that held focus can vanish on a live update; focus moves to its nearest surviving ancestor.
  const focused = useRef<string | null>(null)
  const previousRows = useRef(rows)
  useLayoutEffect(() => {
    const before = previousRows.current
    previousRows.current = rows
    const key = focused.current
    const present = new Set(rows.map((row) => row.key))
    if (!key || present.has(key) || (document.activeElement && document.activeElement !== document.body)) return
    const parents = new Map(before.map((row) => [row.key, row.parentKey]))
    let target = parents.get(key)
    while (target && !present.has(target)) target = parents.get(target)
    target ??= rows[0]?.key
    if (!target) return
    pending.current = target
    setActiveKey(target)
  }, [rows])

  useEffect(() => {
    const key = pending.current
    const element = key ? elements.current.get(key) : undefined
    if (key && element) focusPending(key, element)
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
    const element = elements.current.get(row.key)
    if (element) focusPending(row.key, element)
  }, [virtual, focusPending])

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
    <div ref={container} className={cn('flex min-h-0 min-w-0 flex-col', className)}>
      {shown !== root && (
        <Button
          variant="ghost"
          size="sm"
          className="self-start"
          onClick={() => {
            const opened = isOpen(shown, overrides) ? overrides : new Map(overrides).set(shown.id, true)
            const path = pathTo(root, shown.id) ?? []
            const visible = flattenWorkflow(root, opened).filter((row) => path.includes(row.node.id))
            const target = visible.at(-1)?.key ?? root.id
            pending.current = target
            setActiveKey(target)
            setFocusID(null)
            setOverrides(opened)
          }}
        >
          <ArrowLeft />
          Show the whole workflow
        </Button>
      )}
      <div
        role="tree"
        aria-label={label}
        onKeyDown={onKeyDown}
        onFocus={(event) => {
          const key = event.target.closest<HTMLElement>('[role=treeitem]')?.dataset.key
          if (!key) return
          focused.current = key
          setActiveKey(key)
        }}
        onBlur={(event) => {
          const tree = event.currentTarget
          if (event.relatedTarget) {
            if (!tree.contains(event.relatedTarget)) focused.current = null
            return
          }
          // A removed row blurs with no target and leaves focus on the body, which the restore needs to see.
          window.setTimeout(() => {
            const current = document.activeElement
            if (current && current !== document.body && !tree.contains(current)) focused.current = null
          })
        }}
        className={cn('min-w-0', virtual && 'h-[min(70vh,40rem)]')}
      >
        {virtual ? (
          <VList ref={list} style={{ height: '100%' }} keepMounted={activeIndex === -1 ? undefined : [activeIndex]}>
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
