import { type ReactNode, useEffect, useState } from 'react'
import { needsTask, RunFields, useAgentChoice } from '@/components/launch/run-fields'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Spinner } from '@/components/ui/spinner'
import type { Api } from '@/lib/api'
import { canLaunch } from '@/lib/commands'
import { message } from '@/lib/format'
import type { Workspace } from '@/lib/types'
import { Step } from '@/routes/onboarding/layout'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useSelfRole } from '@/store/hooks'

function useSourceCheck(client: Api, workspaceID: string | undefined) {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const [check, setCheck] = useState<{ workspace: string; error: string | null } | null>(null)
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let live = true
    setCheck(null)
    if (!workspaceID) return
    void (async () => {
      try {
        if (caps.hasMethod('files.tree')) await client.filesTree({ workspace_id: workspaceID, path: '' })
        if (caps.hasMethod('workspace.mirror.status')) {
          const source = await client.workspaceMirrorStatus(workspaceID)
          if (source.enabled && (source.status !== 'ready' || !source.accepted_commit)) {
            throw new Error(source.last_error || `Source is ${source.status ?? 'pending'}; ${isAdmin ? 'verify and adopt the candidate' : 'ask an administrator to verify and adopt the candidate'} in repository setup before launching.`)
          }
        }
        if (live) setCheck({ workspace: workspaceID, error: null })
      } catch (cause) {
        if (live) setCheck({ workspace: workspaceID, error: message(cause) })
      }
    })()
    return () => { live = false }
  }, [client, caps, isAdmin, workspaceID, attempt])
  const current = check?.workspace === workspaceID ? check : null
  return { checked: current !== null, error: current?.error ?? null, retry: () => setAttempt((n) => n + 1) }
}

export function FirstRunStep({
  client,
  workspace,
  back,
  onBackToRepository,
  onBackToAgent,
}: {
  client: Api
  workspace: Workspace | null
  back?: ReactNode
  onBackToRepository: () => void
  onBackToAgent: () => void
}) {
  const navigate = useStore((s) => s.navigate)
  const setOnboarded = useStore((s) => s.setOnboarded)
  const upsertRun = useStore((s) => s.upsertRun)
  const rememberLaunch = useStore((s) => s.rememberLaunch)
  // The draft outlives this component: the header can jump to another step
  // and back, which unmounts it.
  const draft = useStore((s) => s.onboardingFirstRun)
  const setDraft = useStore((s) => s.setOnboardingFirstRun)
  const caps = useCapability()
  const launchable = canLaunch({ cap: caps, role: useSelfRole() })
  const choice = useAgentChoice({ account: '', ownAccountID: '', preferred: draft.harness, client })
  const source = useSourceCheck(client, workspace?.id)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const { harness, mode, agentError, noAgents } = choice

  useEffect(() => {
    const current = useStore.getState().onboardingFirstRun
    if (harness && harness !== current.harness) setDraft({ ...current, harness })
  }, [harness, setDraft])

  const goToBoard = () => {
    setOnboarded(true)
    navigate('board')
  }

  if (!workspace) {
    return (
      <Step
        label="First run"
        title="Start your first run"
        lead="A run is one agent working on its own branch in its own container. It starts from a workspace's base branch, so choose a workspace first."
        actions={<><Button onClick={onBackToRepository}>Choose a repository</Button><Button variant="secondary" onClick={goToBoard}>Go to board</Button>{back}</>}
      />
    )
  }

  const ready = launchable && source.checked && source.error === null && harness !== '' && !needsTask(mode, draft.task)

  const launch = async () => {
    if (!ready) return
    setBusy(true)
    setError(null)
    try {
      const task = draft.task.trim()
      const run = await client.runLaunch({
        workspace_id: workspace.id,
        harness,
        ...(task ? { task } : {}),
        ...(mode === 'tui' ? {} : { mode }),
      })
      rememberLaunch(harness, mode)
      upsertRun(run)
      setOnboarded(true)
      navigate('run', { runId: run.id })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Step
      label="First run"
      title="Start your first run"
      lead={<>A run is one agent working on its own branch in its own container, starting from <Code>{workspace.base_branch}</Code> in {workspace.name}.</>}
      actions={
        <>
          {noAgents ? (
            <Button onClick={onBackToAgent}>Set up an agent</Button>
          ) : (
            <Button disabled={busy || !ready} onClick={() => void launch()}>Launch</Button>
          )}
          <Button variant="secondary" onClick={goToBoard}>Go to board</Button>
          {back}
          {busy && (
            <span className="flex items-center gap-1.5 text-ui-sm text-muted">
              <Spinner label="Starting" />
              Starting the container…
            </span>
          )}
        </>
      }
    >
      {!launchable && (
        <Callout tone="neutral">Your membership or this gateway cannot launch runs. Ask an administrator for launch access; you can still prepare your account.</Callout>
      )}
      {source.error && (
        <Callout
          tone="failed"
          role="alert"
          title="The base branch is not ready"
          actions={<><Button size="sm" variant="secondary" onClick={source.retry}>Check again</Button><Button size="sm" variant="secondary" onClick={onBackToRepository}>Review repository</Button></>}
        >
          <span className="whitespace-pre-wrap">{source.error}</span>
        </Callout>
      )}
      {!source.checked && <p className="text-ui-sm text-muted">Checking the workspace base branch…</p>}
      {agentError && (
        <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={choice.reload}>Retry</Button>}>
          {agentError}
        </Callout>
      )}
      {noAgents ? (
        <Callout tone="neutral" title="No agent is installed yet">
          A run starts an agent, and your environment has none. Set one up, then come back here.
        </Callout>
      ) : (
        <div className="flex min-w-0 flex-col gap-4">
          <RunFields choice={choice} task={draft.task} onTask={(task) => setDraft({ ...useStore.getState().onboardingFirstRun, task })} onSetUp={onBackToAgent} disabled={busy} />
        </div>
      )}
      {error && <Callout tone="failed" role="alert" title="Launch failed" className="whitespace-pre-wrap">{error}</Callout>}
      {!noAgents && (
        <p className="text-ui-sm text-muted">
          After Launch the run starts its container, pulls the image the first time this server uses it, then starts the agent (about 5 s in Enhanced).
        </p>
      )}
    </Step>
  )
}
