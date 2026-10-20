import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { virtualizeAbove, WorkflowTree } from '@/components/workflow/workflow-tree'
import type { SessionItem } from '@/lib/session-types'
import { indexItems, noAgents } from '@/store/subagents'
import { deriveRunWorkflow, type RunFacts, type WorkflowNode, type WorkflowState } from '@/store/workflow'
import { resetItems, tool } from '@/test/acp-stream'
import { sizedViewport } from '@/test/virtual'

beforeEach(resetItems)

function node(id: string, state: WorkflowState, children: WorkflowNode[] = [], over: Partial<WorkflowNode> = {}): WorkflowNode {
  return { id, parentId: null, kind: 'agent', state, label: id, children, ...over }
}

const tree = node('root', 'working', [
  node('a', 'working', [node('a1', 'working'), node('a2', 'completed')]),
  node('b', 'completed', [node('b1', 'completed')]),
  node('c', 'failed'),
], { kind: 'run' })

const items = () => screen.getAllByRole('treeitem')
const focused = () => document.activeElement?.getAttribute('data-key')

describe('WorkflowTree', () => {
  it('exposes a tree with levels, positions and one tab stop', () => {
    render(<WorkflowTree root={tree} label="Run workflow" />)
    const root = screen.getByRole('tree', { name: 'Run workflow' })
    const rows = within(root).getAllByRole('treeitem')
    expect(rows.map((row) => row.dataset.key)).toEqual(['root', 'a', 'a1', 'a2', 'b', 'c'])
    expect(rows.map((row) => row.getAttribute('aria-level'))).toEqual(['1', '2', '3', '3', '2', '2'])
    expect(rows.map((row) => `${row.getAttribute('aria-posinset')}/${row.getAttribute('aria-setsize')}`)).toEqual(['1/1', '1/3', '1/2', '2/2', '2/3', '3/3'])
    expect(rows.map((row) => row.getAttribute('aria-expanded'))).toEqual(['true', 'true', null, null, 'false', null])
    expect(rows.filter((row) => row.tabIndex === 0)).toHaveLength(1)
    expect(rows[5]!.getAttribute('aria-label')).toBe('c, Failed')
    expect(within(rows[5]!).getByRole('img', { name: 'Failed' })).toBeDefined()
  })

  it('moves with the arrows, Home and End, and folds with Left and Right', async () => {
    render(<WorkflowTree root={tree} label="Run workflow" />)
    act(() => items()[0]!.focus())
    await userEvent.keyboard('{ArrowDown}{ArrowDown}')
    expect(focused()).toBe('a1')
    await userEvent.keyboard('{ArrowLeft}')
    expect(focused()).toBe('a')
    await userEvent.keyboard('{ArrowLeft}')
    expect(items().map((row) => row.dataset.key)).toEqual(['root', 'a', 'b', 'c'])
    await userEvent.keyboard('{ArrowRight}')
    expect(items()).toHaveLength(6)
    await userEvent.keyboard('{ArrowRight}')
    expect(focused()).toBe('a1')
    await userEvent.keyboard('{End}')
    expect(focused()).toBe('c')
    await userEvent.keyboard('{Home}')
    expect(focused()).toBe('root')
    expect(items().filter((row) => row.tabIndex === 0).map((row) => row.dataset.key)).toEqual(['root'])
  })

  it('hands Enter to the caller, and folds a parent the caller leaves alone', async () => {
    const onSelect = vi.fn((picked: WorkflowNode) => picked.id === 'c')
    render(<WorkflowTree root={tree} label="Run workflow" onSelect={onSelect} />)
    act(() => items()[5]!.focus())
    await userEvent.keyboard('{Enter}')
    expect(onSelect).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'c' }))
    act(() => items()[4]!.focus())
    await userEvent.keyboard('{Enter}')
    expect(items().map((row) => row.dataset.key)).toContain('b1')
  })

  it('keeps a Needs you descendant in sight under a folded parent', async () => {
    const asking = node('root', 'needs-you', [node('a', 'needs-you', [node('a1', 'needs-you'), node('a2', 'working')])], { kind: 'run' })
    render(<WorkflowTree root={asking} label="Run workflow" />)
    act(() => items()[0]!.focus())
    await userEvent.keyboard('{ArrowLeft}')
    const rows = items()
    expect(rows.map((row) => row.dataset.key)).toEqual(['root', 'root>a1'])
    expect(rows[1]!.getAttribute('aria-level')).toBe('2')
  })

  it('opens a level past the cap in place and comes back', async () => {
    let deep = node('a6', 'working')
    for (let i = 5; i >= 1; i--) deep = node(`a${i}`, 'working', [deep])
    render(<WorkflowTree root={node('root', 'working', [deep], { kind: 'run' })} label="Run workflow" />)
    const nested = screen.getByRole('treeitem', { name: 'Nested workflow · 3 agents' })
    expect(nested.getAttribute('aria-level')).toBe('5')
    fireEvent.click(nested)
    expect(items().map((row) => row.dataset.key)).toEqual(['a3', 'a4', 'a5', 'a6'])
    await userEvent.click(screen.getByRole('button', { name: 'Show the whole workflow' }))
    expect(items()[0]!.dataset.key).toBe('root')
  })

  it('announces a row that comes to need the viewer first, even while the stream keeps changing', () => {
    vi.useFakeTimers()
    try {
      const { rerender } = render(<WorkflowTree root={tree} label="Run workflow" />)
      const status = screen.getByRole('status')
      const done = (id: string) => node(id, 'completed')
      rerender(<WorkflowTree root={{ ...tree, children: [node('a', 'working', [done('a1'), done('a2')]), tree.children[1]!, tree.children[2]!] }} label="Run workflow" />)
      act(() => vi.advanceTimersByTime(1000))
      const next = { ...tree, children: [node('a', 'completed', [done('a1'), done('a2')]), tree.children[1]!, node('c', 'needs-you')] }
      rerender(<WorkflowTree root={next} label="Run workflow" />)
      expect(status.textContent).toBe('')
      act(() => vi.advanceTimersByTime(500))
      expect(status.textContent).toBe('c: Needs you. a1: Completed. a: Completed')
    } finally {
      vi.useRealTimers()
    }
  })

  it('moves focus to the nearest surviving ancestor when the focused row folds away', () => {
    const live = node('root', 'working', [node('a', 'working', [node('a1', 'working')])], { kind: 'run' })
    const { rerender } = render(<WorkflowTree root={live} label="Run workflow" />)
    act(() => screen.getAllByRole('treeitem')[2]!.focus())
    expect(focused()).toBe('a1')
    rerender(<WorkflowTree root={node('root', 'working', [node('a', 'completed', [node('a1', 'completed')])], { kind: 'run' })} label="Run workflow" />)
    expect(items().map((row) => row.dataset.key)).toEqual(['root', 'a'])
    expect(focused()).toBe('a')
  })
})

const facts: RunFacts = { label: 'Big run', state: 'working', reason: 'Agent working', needsYou: false, startedAt: '2026-08-14T10:00:00Z' }

function events(agents: number, total: number): SessionItem[] {
  const out: SessionItem[] = []
  for (let i = 0; i < agents; i++) {
    const parent = i < 50 ? undefined : `a${i % 50}`
    out.push(tool(1, `a${i}`, 'other', `Agent a${i}`, i % 3 === 0 ? 'completed' : 'in_progress', { tool_name: 'Agent', subagent: { description: `Job ${i}` }, parent_id: parent }))
  }
  for (let i = out.length; i < total; i++) out.push(tool(1, `r${i}`, i % 2 ? 'read' : 'execute', `Step ${i}`, 'completed', { parent_id: `a${i % agents}` }))
  return out
}

describe('WorkflowTree budget', () => {
  it('derives and renders 5,000 events across 500 agents within budget', async () => {
    sizedViewport()
    const stream = events(500, 5000)
    const started = performance.now()
    const index = indexItems(noAgents, stream).index
    const root = deriveRunWorkflow('run_big', facts, index)
    const derived = performance.now()
    render(<WorkflowTree root={root} label="Run workflow" />)
    const rendered = performance.now()
    await act(async () => {})
    const mounted = screen.getByRole('tree').querySelectorAll('[role=treeitem]').length
    expect(mounted).toBeGreaterThan(0)
    expect(mounted).toBeLessThan(virtualizeAbove)
    expect(derived - started).toBeLessThan(1000)
    expect(rendered - derived).toBeLessThan(2000)
  })
})
