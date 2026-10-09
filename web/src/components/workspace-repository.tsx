import { useCallback, useRef, useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code, CodeBlock } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { api, type Api } from '@/lib/api'
import { shellQuote } from '@/lib/shell'
import type { Workspace, WorkspaceMirrorResult } from '@/lib/types'
import { actionRow } from '@/routes/onboarding/layout'
import { RepoStep } from '@/routes/onboarding/repo-step'
import { OnboardingSourceOption } from '@/routes/onboarding/source-option'
import { useStore } from '@/store'
import { useIsAdmin, type Capability } from '@/store/hooks'

type WorkspaceRepositoryProps = {
  client?: Api
  caps: Capability
  workspace: Workspace
  initialLocal?: boolean
  onLocalChange?: (local: boolean) => void
  back?: ReactNode
  onNext?: () => void
  advancedOpen?: boolean
}

export function WorkspaceRepository({ client = api, caps, workspace, initialLocal = false, onLocalChange, back, onNext, advancedOpen }: WorkspaceRepositoryProps) {
  const identity = useStore((state) => state.identityKey)
  const epoch = useStore((state) => state.connectionEpoch)
  const isAdmin = useIsAdmin()
  const [local, setLocal] = useState(initialLocal)
  const canReadSource = caps.hasMethod('workspace.mirror.status')
  const context = JSON.stringify([identity, epoch, workspace.id, canReadSource, isAdmin])
  const scope = useRef({ context, client, generation: 0 })
  if (scope.current.context !== context || scope.current.client !== client) {
    scope.current = { context, client, generation: scope.current.generation + 1 }
  }
  const generation = scope.current.generation
  const [ownership, setOwnership] = useState<{ generation: number; status: WorkspaceMirrorResult } | null>(null)
  const source = ownership?.generation === generation ? ownership.status : null
  const onStatusChange = useCallback((status: WorkspaceMirrorResult) => {
    if (scope.current.generation === generation) setOwnership({ generation, status })
  }, [generation])

  const notReady = source?.enabled === true && (source.status !== 'ready' || !source.accepted_commit)

  return <section aria-label="Workspace repository" className="flex min-w-0 flex-col gap-4">
    {notReady && (
      <Callout tone="needs-you" title={`The server's copy is ${source.status ?? 'pending'}`}>
        {source.last_error || 'Runs start once it is verified and adopted. Open Advanced to review it.'}
      </Callout>
    )}
    <div className="flex flex-col items-start gap-2">
      <h3 className="text-ui font-medium text-text">Local clone</h3>
      <p className="text-ui-sm text-muted">
        {source?.enabled
          ? 'Link a clone on this computer to pull run branches. The base branch stays server-owned.'
          : <>Link a clone on this computer and Aether pushes <Code>{workspace.base_branch}</Code> for you. Linking adds an <Code>aether</Code> remote; your history and origin stay as they are.</>}
      </p>
      {!local && <Button size="sm" variant="secondary" onClick={() => { setLocal(true); onLocalChange?.(true) }}>Link local repository</Button>}
      {local && <RepoStep client={client} caps={caps} workspace={workspace} mirrored={source?.enabled === true} sourcePending={source === null} back={back} cancel={<Button size="sm" variant="secondary" onClick={() => { setLocal(false); onLocalChange?.(false) }}>Cancel</Button>} onNext={onNext ?? (() => { setLocal(false); onLocalChange?.(false) })} />}
    </div>
    <div className="border-t border-seam pt-1">
      <Collapsible defaultOpen={advancedOpen}>
        <CollapsibleTrigger>
          <span className="font-medium">Advanced</span>
          <span className="min-w-0 truncate text-muted">Repository source and publishing destination</span>
        </CollapsibleTrigger>
        <CollapsibleContent forceMount className="flex flex-col gap-4 pt-2 pl-5 data-[state=closed]:hidden">
          {canReadSource ? <OnboardingSourceOption key={generation} client={client} workspaceID={workspace.id} canManageSource={isAdmin} onStatusChange={onStatusChange} /> : <p className="text-ui-sm text-muted">This gateway does not offer source status, so source ownership cannot be checked. Linking works, but base pushes stay off until local-only ownership is confirmed; ask an administrator to verify the source with Review source mirror on the Repository page before launching.</p>}
          <div className="flex flex-col gap-1.5">
            <h4 className="text-ui font-medium text-text">Checkout origin</h4>
            <p className="break-words font-code text-ui-sm">{workspace.origin || 'Not configured; new run checkouts have no publishing remote.'}</p>
            <p className="text-ui-sm text-muted">The upstream URL a run checkout publishes to, not the read-only source. To change it, run this on your linked computer with the intended URL:</p>
            <CodeBlock className="whitespace-pre-wrap break-words">{`aether workspace origin --workspace ${shellQuote(workspace.id)} https://your-git-host/your-team/your-repository.git`}</CodeBlock>
          </div>
          <p className="text-ui-sm text-muted">A read-only deploy key only lets the server fetch. To publish branches or pull requests, set up your own Git or gh credentials in your environment and get upstream permission.</p>
        </CollapsibleContent>
      </Collapsible>
    </div>
    {!local && (onNext || back) && <div className={actionRow}>
      {onNext && <Button onClick={onNext}>Continue</Button>}
      {back}
    </div>}
  </section>
}
