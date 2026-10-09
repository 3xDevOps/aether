import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { FolderGit2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Skeleton } from '@/components/ui/skeleton'
import { WorkspaceCreate } from '@/components/workspace-create'
import { WorkspaceRepository } from '@/components/workspace-repository'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { Workspace } from '@/lib/types'
import { GitIdentityForm } from '@/routes/onboarding/git-identity'
import { Step } from '@/routes/onboarding/layout'
import { useStore } from '@/store'
import { type Capability, useIsAdmin } from '@/store/hooks'

export function RepositoryStep({
  client,
  caps,
  workspace,
  local,
  onChoose,
  onLocalChange,
  back,
  onNext,
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  local: boolean
  onChoose: (workspace: Workspace, source?: 'local' | 'remote', accepted?: boolean) => void
  onLocalChange: (local: boolean) => void
  back?: ReactNode
  onNext: () => void
}) {
  const isAdmin = useIsAdmin()
  const [workspaces, setWorkspaces] = useState<Workspace[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [choosing, setChoosing] = useState(workspace === null)
  const fetchVersion = useRef(0)
  const hosted = !caps.hasLocal('link.status')

  const refetch = useCallback(() => {
    const version = ++fetchVersion.current
    setError(null)
    return client.workspaceListFull().then((list) => {
      if (version !== fetchVersion.current) return
      setWorkspaces(list)
      useStore.getState().setWorkspaces(list)
    }).catch((err) => { if (version === fetchVersion.current) setError(message(err)) })
  }, [client])

  useEffect(() => {
    void refetch()
    return () => { fetchVersion.current += 1 }
  }, [refetch])

  const loading = useDelayed(workspaces === null && error === null)
  const canAdd = isAdmin && (caps.hasMethod('workspace.add') || caps.hasMethod('workspace.import'))
  const askAdmin = workspaces?.length === 0 && !canAdd
  const choose = (next: Workspace, source?: 'local' | 'remote', accepted?: boolean) => {
    setChoosing(false)
    onChoose(next, source, accepted)
  }

  const identity = hosted && caps.hasMethod('member.git') && (
    <section aria-labelledby="repository-git-identity" className="flex flex-col gap-2 border-b border-seam pb-4">
      <h3 id="repository-git-identity" className="text-ui font-medium text-text">Git identity</h3>
      <GitIdentityForm client={client} caps={caps} />
    </section>
  )

  if (workspace && !choosing) {
    return (
      <Step
        label="Repository"
        title="Choose a repository"
        lead="A workspace is one repository and base branch, and the runs started from it."
      >
        {identity}
        <div className="flex min-w-0 items-center gap-2 rounded-panel border border-seam px-3 py-2">
          <FolderGit2 aria-hidden className="size-4 shrink-0 text-muted" />
          <span className="min-w-0 truncate text-ui font-medium text-text">{workspace.name}</span>
          <Code>{workspace.base_branch}</Code>
          <span className="flex-1" />
          <Button size="sm" variant="ghost" aria-label="Choose another workspace" onClick={() => setChoosing(true)}>
            Change
          </Button>
        </div>
        <WorkspaceRepository
          key={workspace.id}
          client={client}
          caps={caps}
          workspace={workspace}
          back={back}
          initialLocal={local}
          onNext={onNext}
          onLocalChange={onLocalChange}
        />
      </Step>
    )
  }

  return (
    <Step
      label="Repository"
      title="Choose a repository"
      lead="A workspace is one repository and base branch, and the runs started from it."
      actions={(askAdmin || back) && (
        <>
          {askAdmin && <Button onClick={onNext}>Continue to Agent</Button>}
          {workspace && <Button variant="secondary" onClick={() => setChoosing(false)}>Keep {workspace.name}</Button>}
          {back}
        </>
      )}
    >
      {identity}
      {loading && <div className="h-16"><Skeleton className="size-full" /></div>}
      {error && (
        <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={() => void refetch()}>Retry workspace list</Button>}>
          {error}
        </Callout>
      )}
      {workspaces && workspaces.length > 0 && (
        <section aria-labelledby="repository-workspaces" className="flex min-w-0 flex-col gap-2">
          <h3 id="repository-workspaces" className="text-ui font-medium text-text">Your workspaces</h3>
          <ul className="flex min-w-0 flex-col divide-y divide-seam rounded-panel border border-seam">
            {workspaces.map((w) => (
              <li key={w.id} className="flex min-w-0 items-center gap-2 px-3 py-2">
                <FolderGit2 aria-hidden className="size-4 shrink-0 text-muted" />
                <span className="min-w-0 flex-1 truncate text-ui font-medium text-text">{w.name}</span>
                <Code>{w.base_branch}</Code>
                <Button size="sm" variant="secondary" aria-label={`Use ${w.name}`} onClick={() => choose(w)}>
                  Use
                </Button>
              </li>
            ))}
          </ul>
        </section>
      )}
      {askAdmin && (
        <Callout tone="needs-you" title="Ask an admin to add a workspace">
          Only an admin can add a repository to this server. You can set up an agent in the meantime.
        </Callout>
      )}
      <WorkspaceCreate client={client} onCreated={choose} onRefresh={() => void refetch()} />
    </Step>
  )
}
