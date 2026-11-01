import { useState } from 'react'
import { toast } from 'sonner'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { AskAssistantButton } from '@/components/assistant/ask'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { AuthMethod, SessionItem } from '@/lib/session-types'
import type { RunNavigation } from '@/routes/run/header'
import type { RunShells } from '@/routes/run/shells'
import { useStore } from '@/store'
import { isTerminal, type RunRecord } from '@/store/runs'
import type { AcpSession } from '@/store/sessions'
import { sessionLease } from '@/store/session-stream'

const adapterFailed = ['Enhanced agent failed', 'Enhanced agent exited']
const authError = /authentication required|authrequired|not logged in|not signed in/i

export type SessionFailure =
  | { kind: 'adapter'; title: string; detail?: string; seq?: number }
  | { kind: 'auth'; detail?: string; seq?: number }

export function sessionFailure(session: AcpSession | undefined): SessionFailure | null {
  if (!session) return null
  let notice: SessionItem | undefined
  scan: for (let t = session.turns.length - 1; t >= 0; t--) {
    const items = session.turns[t]!.items
    for (let i = items.length - 1; i >= 0; i--) {
      const item = items[i]!
      if (item.kind === 'turn_start') break scan
      if (item.kind === 'notice' && item.notice && (adapterFailed.includes(item.notice.title) || item.notice.title === 'Prompt failed')) {
        notice = item
        break scan
      }
    }
  }
  const authNone = session.state?.auth?.authStatus?.kind === 'none'
  const detail = notice?.notice?.description
  if ((detail && authError.test(detail)) || authNone) return { kind: 'auth', detail, seq: notice?.seq }
  if (notice && !session.live && adapterFailed.includes(notice.notice!.title)) return { kind: 'adapter', title: notice.notice!.title, detail, seq: notice.seq }
  return null
}

function OpenInStandard({ run, switchable, nav }: { run: RunRecord; switchable: boolean; nav: RunNavigation }) {
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const act = async () => {
    setBusy(true)
    try {
      if (switchable) {
        useStore.getState().upsertRun(await api.runModeSwitch(run.id, 'tui', sessionLease(useStore, run.id)))
        nav.go('terminal')
        return
      }
      // The run's owner keeps the account they chose, its fallback and its
      // child-run setting; a teammate's launch runs on their own, as any
      // launch of theirs does.
      const own = run.member_id === useStore.getState().info?.member.id
      const account = run.requested_account_member_id ?? run.account_member_id
      const extras = run.repos?.filter((repo) => !repo.primary).map((repo) => repo.workspace_id) ?? []
      const fallback = run.no_fallback ? { no_fallback: true } : run.fallback_account_member_ids?.length ? { fallback_account_member_ids: run.fallback_account_member_ids } : {}
      const next = await api.runLaunch({
        workspace_id: run.workspace_id,
        task: run.task,
        harness: run.harness,
        mode: 'tui',
        ...(run.model ? { model: run.model } : {}),
        ...(run.effort ? { effort: run.effort } : {}),
        ...(own && account && account !== run.member_id ? { account_member_id: account } : {}),
        ...(own ? fallback : {}),
        ...(own && run.children ? { children: run.children } : {}),
        ...(extras.length ? { extra_workspace_ids: extras } : {}),
      })
      useStore.getState().upsertRun(next)
      useStore.getState().upsertRun(await api.runClose(run.id, 'abandoned'))
      useStore.getState().navigate('run', { runId: next.id })
    } catch (err) {
      toast.error(`Open in Standard failed: ${errorSentence(err)}`)
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <Button
        size="sm"
        variant="secondary"
        disabled={busy}
        hint={switchable ? 'Resumes this conversation in the agent’s own terminal' : 'Starts a new Standard session with the same task and closes this one'}
        onClick={() => (switchable ? void act() : setConfirm(true))}
      >
        Open in Standard
      </Button>
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Start a Standard session instead?</AlertDialogTitle>
            <AlertDialogDescription>
              This agent cannot move a running conversation between modes. Aether starts a new Standard session with the same task and closes this one without merging. The conversation so far stays in this session’s history.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep this session</AlertDialogCancel>
            <AlertDialogAction onClick={() => void act()}>Start a Standard session</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function LoginActions({ run, command, methods, shells, nav }: { run: RunRecord; command: string | undefined; methods: AuthMethod[]; shells: RunShells; nav: RunNavigation }) {
  const terminal = methods.filter((method) => method.type === 'terminal')
  const openShell = async (typed?: string) => {
    nav.go('terminal')
    const tab = await shells.open()
    const lease = sessionLease(useStore, run.id)
    if (!tab || !typed || !lease) return
    await api.devTerminalInput({ run_id: run.id, terminal_id: tab.terminal_id, incarnation: tab.incarnation, kind: 'text', text: typed, ...lease })
      .catch((err: unknown) => toast.error(`Could not type the login command: ${errorSentence(err)}`))
  }
  return (
    <>
      {terminal.map((method) => (
        <Button key={method.id} size="sm" onClick={() => void openShell(command && [command, ...(method.args ?? [])].join(' '))}>
          {method.name}
        </Button>
      ))}
      <Button size="sm" variant={terminal.length ? 'secondary' : 'primary'} disabled={!shells.canAdd} onClick={() => void openShell()}>
        Open a terminal
      </Button>
    </>
  )
}

export function SessionFailureCallout({ run, session, switchable, shells, nav }: {
  run: RunRecord
  session: AcpSession | undefined
  switchable: boolean
  shells: RunShells
  nav: RunNavigation
}) {
  const [retrying, setRetrying] = useState(false)
  const failure = sessionFailure(session)
  if (!failure || isTerminal(run.status)) return null
  const target = { kind: 'failure' as const, workspace_id: run.workspace_id, run_id: run.id, failure: failure.kind, seq: failure.seq, label: failure.kind === 'auth' ? 'Agent not signed in' : failure.title }
  if (failure.kind === 'auth') {
    const label = session?.state?.auth?.authStatus?.label
    const methods = session?.state?.auth_methods ?? []
    return (
      <Callout
        tone="needs-you"
        title="The agent is not signed in"
        actions={
          <>
            <LoginActions run={run} command={session?.state?.agent_command} methods={methods} shells={shells} nav={nav} />
            <AskAssistantButton size="sm" variant="secondary" ask={{ draft: 'Help me sign the agent in', attachments: [target] }}>Ask the assistant</AskAssistantButton>
          </>
        }
      >
        <p>{label ? `${label}. ` : ''}Log the agent in from a terminal in this session, then send your message again.</p>
        {methods.length > 0 && (
          <ul className="mt-1 list-disc pl-5 text-ui-sm">
            {methods.map((method) => <li key={method.id}>{method.name}{method.description ? `: ${method.description}` : ''}</li>)}
          </ul>
        )}
        {failure.detail && <pre className="mt-1 font-code text-ui-sm break-words whitespace-pre-wrap text-muted">{failure.detail}</pre>}
      </Callout>
    )
  }
  const retry = async () => {
    setRetrying(true)
    try {
      await api.runPause(run.id)
      await api.runResume(run.id)
    } catch (err) {
      toast.error(`Retry Enhanced failed: ${errorSentence(err)}`)
    } finally {
      setRetrying(false)
    }
  }
  return (
    <Callout
      tone="failed"
      title={failure.title}
      actions={
        <>
          <Button size="sm" disabled={retrying} hint="Pauses and resumes the session, which starts the agent again" onClick={() => void retry()}>
            {retrying ? 'Retrying…' : 'Retry Enhanced'}
          </Button>
          <OpenInStandard run={run} switchable={switchable} nav={nav} />
          <AskAssistantButton size="sm" variant="secondary" ask={{ draft: 'Fix this', attachments: [target] }}>Fix this</AskAssistantButton>
        </>
      }
    >
      {failure.detail && <pre className="max-h-40 overflow-y-auto font-code text-ui-sm break-words whitespace-pre-wrap">{failure.detail}</pre>}
    </Callout>
  )
}
