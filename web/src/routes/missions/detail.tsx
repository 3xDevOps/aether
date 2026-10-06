import { useEffect, useMemo, useState } from 'react'
import { Ellipsis } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { RelativeTime } from '@/components/ui/relative-time'
import { Skeleton } from '@/components/ui/skeleton'
import { StateLine, type Tone } from '@/components/ui/status-dot'
import { ViewHeader } from '@/components/view-header'
import { ApiError, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { needsYouConditions, type NeedsYouID } from '@/lib/needs-you'
import { allowed } from '@/lib/permissions'
import { presentRun } from '@/lib/status'
import type { AgentInfo, Mission } from '@/lib/types'
import { AgentMessages } from '@/routes/missions/agent-messages'
import { ClampedText } from '@/routes/missions/clamped-text'
import { CancelSwarm, ReplaceIntegrator } from '@/routes/missions/dialogs'
import { SwarmIntegration } from '@/routes/missions/integration'
import { answerFormID, SwarmQuestions } from '@/routes/missions/questions'
import {
  integratorLabel,
  missionFinal,
  objectiveTitle,
  phaseSentence,
  phaseTone,
  phaseWord,
} from '@/routes/missions/swarm'
import { SwarmTasks } from '@/routes/missions/tasks'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { MissionDetail } from '@/store/missions'
import { isTerminal } from '@/store/runs'
import { stateContextOf } from '@/store/selectors'

type Lookup = 'checking' | 'found' | 'missing' | { error: string }

/** A run absent from the hydrated store may only lag its create; only the server's not-found marks it missing. */
function useIntegratorLookup(runID: string | undefined, client: Api, detail: MissionDetail | undefined): Lookup | undefined {
  const present = useStore((s) => Boolean(runID && s.runs[runID]))
  const hydrated = useStore((s) => s.hydrated)
  const upsertRun = useStore((s) => s.upsertRun)
  const [lookups, setLookups] = useState<Record<string, Lookup>>({})
  useEffect(() => {
    setLookups((current) => Object.fromEntries(Object.entries(current).filter(([, lookup]) => typeof lookup === 'string')))
  }, [detail])
  useEffect(() => {
    if (!hydrated || !runID || present || lookups[runID]) return
    setLookups((current) => ({ ...current, [runID]: 'checking' }))
    client.runGet(runID).then((run) => {
      upsertRun(run)
      setLookups((current) => ({ ...current, [runID]: 'found' }))
    }).catch((err) => {
      const lookup = err instanceof ApiError && err.status === 404 ? 'missing' : { error: message(err) }
      setLookups((current) => ({ ...current, [runID]: lookup }))
    })
  }, [client, hydrated, present, runID, lookups, upsertRun])
  return hydrated && runID && !present ? lookups[runID] : undefined
}

function useIntegratorLine(runID: string | undefined) {
  const now = useClock()
  const key = useStore((s) => {
    const run = runID ? s.runs[runID] : undefined
    if (!run) return ''
    const ctx = stateContextOf(s, now)
    const shown = presentRun(run, ctx)
    const problem = isTerminal(run.status) ? undefined : needsYouConditions.find((c) => !swarmConditions.has(c.id) && c.applies(run, ctx))
    return JSON.stringify([shown.reason, shown.state === 'needs-you', shown.unread ?? 0, isTerminal(run.status), problem?.reason(run, ctx) ?? ''])
  })
  return useMemo(() => {
    if (!key) return undefined
    const [reason, needsYou, unread, stopped, problem] = JSON.parse(key) as [string, boolean, number, boolean, string]
    return { reason, needsYou, unread, stopped, problem }
  }, [key])
}

const swarmConditions = new Set<NeedsYouID>(['swarm-question', 'integrator-down'])

function stateLineOf(mission: Mission, line: ReturnType<typeof useIntegratorLine>, missing: boolean): { tone: Tone; text: string } {
  const final = missionFinal(mission)
  if (!final && mission.integrator_launch_error) return { tone: 'needs-you', text: 'Integrator failed to launch' }
  if (!final && (missing || line?.stopped)) return { tone: 'needs-you', text: 'Integrator stopped' }
  const questions = `${mission.open_questions} question${mission.open_questions === 1 ? '' : 's'} for you`
  if (!final && line?.problem) {
    return { tone: 'needs-you', text: mission.open_questions > 0 ? `${line.problem} · ${questions}` : line.problem }
  }
  if (!final && mission.open_questions > 0) return { tone: 'needs-you', text: `${phaseWord[mission.phase]} · ${questions}` }
  if (!final && line?.needsYou) return { tone: 'needs-you', text: line.reason }
  if (!final && line?.unread) return { tone: phaseTone[mission.phase], text: line.reason }
  return { tone: phaseTone[mission.phase], text: phaseWord[mission.phase] }
}

function Recovery({ mission, stopped, missing, canReplace, onReplace }: {
  mission: Mission
  stopped: boolean
  missing: boolean
  canReplace: boolean
  onReplace: () => void
}) {
  if (missionFinal(mission) || !(stopped || missing || mission.integrator_launch_error)) return null
  const body = missing
    ? mission.integrator_run_launched
      ? 'The integrator run was deleted; replace the integrator or cancel the swarm.'
      : 'The integrator run has not started. The server retries the launch periodically and logs each failure as "mission: recover integrator".'
    : stopped
      ? 'The integrator run has exited; replace the integrator to continue.'
      : undefined
  return (
    <Callout
      tone="needs-you"
      title={stopped || missing ? 'The integrator is not running' : 'The integrator did not launch'}
      actions={canReplace && <Button size="sm" variant="secondary" onClick={onReplace}>Replace integrator…</Button>}
    >
      {body && <p>{body}</p>}
      {mission.integrator_launch_error && (
        <p className="text-state-failed">
          Last launch failure
          {mission.integrator_launch_error_at && <> <RelativeTime at={mission.integrator_launch_error_at} /></>}
          : {mission.integrator_launch_error}
        </p>
      )}
    </Callout>
  )
}

export function SwarmDetail({ missionID, detail, agents, error, loading, client, onChanged }: {
  missionID: string
  detail?: MissionDetail
  agents: AgentInfo[] | null
  error: string | null
  loading: boolean
  client: Api
  onChanged: () => void
}) {
  const navigate = useStore((s) => s.navigate)
  const mobile = useIsMobile()
  const self = useSelf()
  const cap = useCapability()
  const [dialog, setDialog] = useState<'replace' | 'cancel' | null>(null)
  const closeDialog = () => setDialog(null)
  const mission = detail?.mission.id === missionID ? detail.mission : undefined
  const integratorRunID = mission?.current_integrator_run_id || undefined
  const lookup = useIntegratorLookup(integratorRunID, client, detail)
  const line = useIntegratorLine(integratorRunID)
  const missing = lookup === 'missing'

  if (!mission || !detail) {
    return (
      <div className="flex h-full min-h-0 min-w-0 flex-col">
        <ViewHeader title="Swarm" />
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 px-4 py-6 sm:px-6">
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
          {loading && !error && <div className="h-24"><Skeleton className="size-full" /></div>}
        </div>
      </div>
    )
  }

  const final = missionFinal(mission)
  const launcher = allowed('launch', self)
  const humanAuthority = launcher && (self.id === mission.accountable_human_id || self.role === 'admin')
  const canAnswer = cap.hasMethod('mission.question.answer') && humanAuthority
  const canReplace = !final && cap.hasMethod('mission.replace-integrator') && launcher
  const canCancel = !final && cap.hasMethod('mission.cancel') && humanAuthority
  const canRelease = cap.hasMethod('mission.worker.release') && launcher
  const openQuestion = canAnswer ? detail.questions.find((question) => !question.answered_at) : undefined
  const state = stateLineOf(mission, line, missing)
  const title = objectiveTitle(mission.objective)
  const [first, ...more] = mission.objective.trim().split('\n')
  const rest = (first.trim() === title ? more.join('\n') : mission.objective).trim()
  const stateLine = (
    <StateLine tone={state.tone}>
      {state.text} · {integratorLabel(mission, agents)} · created <RelativeTime at={mission.created_at} />
    </StateLine>
  )

  const answer = () => {
    const field = openQuestion && document.getElementById(answerFormID(openQuestion))?.querySelector('textarea')
    field?.scrollIntoView({ block: 'center' })
    field?.focus()
  }

  const actions = (
    <>
      {openQuestion ? (
        !mobile && <Button size="sm" onClick={answer}>Answer</Button>
      ) : (
        integratorRunID && !missing && (
          <Button size="sm" variant={final || mobile ? 'secondary' : 'primary'} onClick={() => navigate('run', { runId: integratorRunID })}>
            Open integrator
          </Button>
        )
      )}
      {(canReplace || canCancel) && (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" label="More swarm actions">
              <Ellipsis />
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            {canReplace && <MenuItem onSelect={() => setDialog('replace')}>Replace integrator…</MenuItem>}
            {canCancel && <MenuItem tone="danger" onSelect={() => setDialog('cancel')}>Cancel swarm…</MenuItem>}
          </MenuContent>
        </Menu>
      )}
    </>
  )

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title={title} subtitle={stateLine} actions={mobile ? undefined : actions} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-6 px-4 py-6 sm:px-6">
          <section aria-label="Swarm" className="flex flex-col gap-2">
            {mobile && (
              <>
                <div className="flex items-start gap-2">
                  <p className="min-w-0 flex-1 text-title">{title}</p>
                  {actions}
                </div>
                {stateLine}
              </>
            )}
            {rest && <div className="text-ui"><ClampedText text={rest} /></div>}
            <p className="text-ui text-muted">{phaseSentence[mission.phase]}</p>
            {error && <Callout tone="failed" role="alert">{error}</Callout>}
            {typeof lookup === 'object' && <Callout tone="failed" role="alert">{lookup.error}</Callout>}
            <Recovery
              mission={mission}
              stopped={Boolean(line?.stopped)}
              missing={missing}
              canReplace={canReplace}
              onReplace={() => setDialog('replace')}
            />
            {!final && line?.problem && integratorRunID && !mission.integrator_launch_error && (
              <Callout
                tone="needs-you"
                title="The integrator needs you"
                actions={openQuestion && <Button size="sm" variant="secondary" onClick={() => navigate('run', { runId: integratorRunID })}>Open integrator</Button>}
              >
                <p className="break-words">{line.problem}</p>
              </Callout>
            )}
          </section>
          <SwarmQuestions
            questions={detail.questions}
            canAnswer={canAnswer}
            accountableID={mission.accountable_human_id}
            client={client}
            onAnswered={onChanged}
          />
          <SwarmTasks detail={detail} agents={agents} canRelease={canRelease} client={client} onChanged={onChanged} />
          <AgentMessages detail={detail} client={client} />
          <SwarmIntegration detail={detail} agents={agents} client={client} />
        </div>
      </div>
      {dialog === 'replace' && (
        <ReplaceIntegrator
          mission={mission}
          agents={agents}
          client={client}
          onClose={closeDialog}
          onReplaced={() => {
            closeDialog()
            onChanged()
          }}
        />
      )}
      {dialog === 'cancel' && (
        <CancelSwarm
          mission={mission}
          client={client}
          onClose={closeDialog}
          onCancelled={() => {
            closeDialog()
            onChanged()
          }}
        />
      )}
    </div>
  )
}
