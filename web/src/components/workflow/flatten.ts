import { needsYouBelow, startsOpen, type WorkflowNode } from '@/store/workflow'

/** Deeper levels collapse into one Nested workflow row that opens in place. */
export const maxDepth = 4

export interface WorkflowRow {
  /** Unique among rows: a surfaced row carries its folded ancestor's id. */
  key: string
  /** For a Nested workflow row, the node whose subtree it opens. */
  node: WorkflowNode
  level: number
  posinset: number
  setsize: number
  /** Undefined for a leaf. */
  expanded?: boolean
  /** A Needs you descendant shown under its folded ancestor. */
  surfaced?: boolean
  /** The hidden subtree's size, for the Nested workflow row. */
  nested?: number
  /** Per ancestor level, whether its connector continues past this row. */
  guides: boolean[]
  last: boolean
  parentKey?: string
}

function size(nodes: WorkflowNode[]): number {
  return nodes.reduce((sum, node) => sum + 1 + size(node.children), 0)
}

export function isOpen(node: WorkflowNode, overrides: ReadonlyMap<string, boolean>): boolean {
  return overrides.get(node.id) ?? startsOpen(node)
}

export function flattenWorkflow(root: WorkflowNode, overrides: ReadonlyMap<string, boolean>): WorkflowRow[] {
  const rows: WorkflowRow[] = []
  const walk = (nodes: WorkflowNode[], parent: WorkflowNode | undefined, level: number, guides: boolean[]) => {
    if (parent && level > maxDepth) {
      rows.push({ key: `nested:${parent.id}`, node: parent, level, posinset: 1, setsize: 1, nested: size(nodes), guides, last: true, parentKey: parent.id })
      return
    }
    nodes.forEach((node, i) => {
      const last = i === nodes.length - 1
      const branch = node.children.length > 0
      const expanded = branch ? isOpen(node, overrides) : undefined
      rows.push({ key: node.id, node, level, posinset: i + 1, setsize: nodes.length, expanded, guides, last, parentKey: parent?.id })
      const below = [...guides, !last]
      if (expanded) {
        walk(node.children, node, level + 1, below)
        return
      }
      const asking = branch ? needsYouBelow(node) : []
      asking.forEach((child, j) => {
        rows.push({
          key: `${node.id}>${child.id}`,
          node: child,
          level: level + 1,
          posinset: j + 1,
          setsize: asking.length,
          surfaced: true,
          guides: below,
          last: j === asking.length - 1,
          parentKey: node.id,
        })
      })
    })
  }
  walk([root], undefined, 1, [])
  return rows
}
