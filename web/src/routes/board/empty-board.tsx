import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { api, ApiError } from '@/lib/api'
import { canLaunch } from '@/lib/commands'
import { message } from '@/lib/format'
import type { AgentsState } from '@/routes/agents/use-agents'
import { useStore } from '@/store'
import type { OnboardingStep } from '@/store/ui'
import { useCapability, useSelfRole } from '@/store/hooks'

// files.tree answers protocol.CodeUnavailable only when the workspace has no repository yet.
const codeUnavailable = -32004

type BaseCheck = { workspace: string; missing: boolean; error: string | null }

export function EmptyBoard({ agents, hiddenByMine }: { agents: AgentsState; hiddenByMine: boolean }) {
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
        action={<Button onClick={() => onboard('Repository')}>Add your repository</Button>}
      >
        A workspace is a repository and the base branch every run starts from.
      </EmptyState>
    )
  }
  if (workspace && base === undefined) return null
  if (workspace && base?.error && !base.missing) {
    return (
      <div className="p-4">
        <Callout role="alert" tone="failed" title="Cannot check the base branch">
          {base.error}
        </Callout>
      </div>
    )
  }
  if (workspace && base?.missing) {
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
  if (hiddenByMine) {
    return (
      <EmptyState
        title="No runs of yours"
        action={<Button onClick={() => useStore.getState().setMineOnly(false)}>Show everyone's runs</Button>}
      >
        Mine is on, so runs started by teammates are hidden.
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
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    const recheck = () => setAttempt((n) => n + 1)
    window.addEventListener('focus', recheck)
    return () => window.removeEventListener('focus', recheck)
  }, [])
  useEffect(() => {
    if (!workspaceID || !checks) return
    let live = true
    api
      .filesTree({ workspace_id: workspaceID, path: '' })
      .then(() => live && setCheck({ workspace: workspaceID, missing: false, error: null }))
      .catch(
        (err: unknown) =>
          live &&
          setCheck({
            workspace: workspaceID,
            missing: err instanceof ApiError && err.code === codeUnavailable,
            error: message(err),
          }),
      )
    return () => {
      live = false
    }
  }, [workspaceID, checks, attempt])
  if (!checks) return null
  return check?.workspace === workspaceID ? check : undefined
}
