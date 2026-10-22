import { memberName, resolveMember } from '@/lib/member-name'
import { runLabel } from '@/lib/status'
import type { SystemContainer } from '@/lib/types'
import { useStore } from '@/store'

export function useNames() {
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const workspaces = useStore((s) => s.workspaces)
  const names = { members, mergedInto }
  const member = (id: string) => memberName(names, id)
  return {
    runs,
    member,
    owner: (kind: string, id: string): string | undefined =>
      kind === 'run' ? runs[id] && runLabel(runs[id]) : kind === 'workspace' ? workspaces[id]?.name : kind === 'member' && resolveMember(names, id).member ? member(id) : undefined,
    container: (c: SystemContainer) => {
      if (c.owner_kind === 'member') return `Environment · ${member(c.owner_id)}`
      const run = runs[c.owner_id] ? runLabel(runs[c.owner_id]) : c.title || c.owner_id
      return c.owner_kind === 'browser' ? `Browser · ${run}` : run
    },
  }
}
