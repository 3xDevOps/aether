import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { api } from '@/lib/api'
import { canLaunch } from '@/lib/commands'
import { message } from '@/lib/format'
import type { AgentInfo } from '@/lib/types'
import { useStore } from '@/store'
import type { OnboardingStep } from '@/store/ui'
import { useCapability, useSelfRole } from '@/store/hooks'

type BaseCheck = { workspace: string; error: string | null }

export type AgentsState = AgentInfo[] | 'loading' | 'unknown'

export function EmptyBoard({ agents }: { agents: AgentsState }) {
  const caps = useCapability()
  const role = useSelfRole()
  const hasWorkspace = useStore((s) => Object.keys(s.workspaces).length > 0)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const openDialog = useStore((s) => s.openPaletteDialog)
  const base = useBaseCheck(workspace?.id)

  const onboard = (step: OnboardingStep) => {
    const state = useStore.getState()
    if (workspace) state.setOnboardingWorkspace(workspace.id)
    state.setOnboardingStep(step)
    state.navigate('onboarding')
  }

  if (!hasWorkspace) {
    return (
      <EmptyState
        title="No workspace yet"
        action={<Button onClick={() => onboard('Workspace')}>Add your repository</Button>}
      >
        A workspace is a repository and the base branch every run starts from.
      </EmptyState>
    )
  }
  if (workspace && base === undefined) return null
  if (workspace && base?.error) {
    return (
      <EmptyState
        title="Base branch missing"
        action={<Button onClick={() => onboard('Repository')}>Push your base branch</Button>}
      >
        Runs start from <code className="font-code">{workspace.base_branch}</code>, which the server cannot read yet:{' '}
        {base.error}
      </EmptyState>
    )
  }
  if (agents === 'loading') return null
  if (Array.isArray(agents) && !agents.some((agent) => agent.installed)) {
    return (
      <EmptyState
        title="No agent installed"
        action={<Button onClick={() => useStore.getState().navigate('agents')}>Set up an agent</Button>}
      >
        An agent is the coding CLI a run starts, such as Claude Code or Codex.
      </EmptyState>
    )
  }
  return (
    <EmptyState
      title="No runs yet"
      action={canLaunch({ cap: caps, role }) && <Button onClick={() => openDialog('launch')}>New run</Button>}
    >
      A run is one agent working on its own branch in its own container.
    </EmptyState>
  )
}

function useBaseCheck(workspaceID: string | undefined): BaseCheck | null | undefined {
  const caps = useCapability()
  const checks = caps.hasMethod('files.tree')
  const [check, setCheck] = useState<BaseCheck | null>(null)
  useEffect(() => {
    if (!workspaceID || !checks) return
    let live = true
    api
      .filesTree({ workspace_id: workspaceID, path: '' })
      .then(() => live && setCheck({ workspace: workspaceID, error: null }))
      .catch((err: unknown) => live && setCheck({ workspace: workspaceID, error: message(err) }))
    return () => {
      live = false
    }
  }, [workspaceID, checks])
  if (!checks) return null
  return check?.workspace === workspaceID ? check : undefined
}
