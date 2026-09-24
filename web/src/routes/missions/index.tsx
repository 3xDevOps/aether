import { useEffect, useMemo, useRef, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import { ViewHeader } from '@/components/view-header'
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
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { message } from '@/lib/format'
import { api, ApiError, type Api } from '@/lib/api'
import { allowed } from '@/lib/permissions'
import type {
  Mission,
  MissionAttempt,
  MissionShowResult,
  MissionScopeDiagnostic,
  MissionSubmission,
  MissionTask,
  MissionTaskScope,
} from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, usePendingApprovalRuns, useSelf } from '@/store/hooks'
import { registerSlot } from '@/components/slots'
import type { CardSlotProps } from '@/components/slots'
import { Chip } from '@/components/ui/heroui'
import { StatusChip } from '@/components/run-list'
import { runState } from '@/lib/status'
import { CandidateReview } from '@/routes/terminal/candidate-review'
import {
  AmendmentReviewSection,
  ErrorNotice,
  PhaseBanner,
  PhaseChip,
  PlanningHistory,
  missionPhase,
  PlanReviewSection,
  QuestionsSection,
} from '@/routes/missions/plan-gate'

function newIdempotencyKey(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `mission-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

const statusLabel: Record<MissionTask['status'], string> = {
  ready: 'Ready',
  working: 'Working',
  review: 'Review',
  done: 'Done',
  proposed: 'Proposed',
  abandoned: 'Abandoned',
  blocked: 'Blocked',
}

const statusColor: Record<MissionTask['status'], 'accent' | 'default' | 'success' | 'warning' | 'danger'> = {
  ready: 'accent',
  working: 'warning',
  review: 'warning',
  done: 'success',
  proposed: 'default',
  abandoned: 'danger',
  blocked: 'danger',
}

function missionStatus(mission: Mission, tasks: MissionTask[], submissions: MissionSubmission[]): string {
  switch (missionPhase(mission)) {
    case 'planning':
      return 'Planning'
    case 'clarified':
      return 'Preparing plan'
    case 'plan_review':
      return 'Plan ready for review'
    case 'amendment_review':
      return 'Amendment ready for review'
    case 'rejected':
      return 'Rejected'
  }
  if (!tasks.length) return 'Preparing'
  const acceptedDone = tasks.every(
    (task) =>
      task.status === 'done' &&
      submissions.some(
        (submission) =>
          submission.state === 'accepted' &&
          submission.task_id === task.id &&
          submission.task_revision === task.current_revision,
      ),
  )
  if (acceptedDone) return 'Tasks accepted'
  if (tasks.some((task) => task.status === 'working')) return 'Working'
  if (tasks.some((task) => task.status === 'blocked')) return 'Blocked'
  if (tasks.some((task) => task.status === 'review') || tasks.every((task) => task.status === 'done')) return 'Review'
  if (tasks.some((task) => task.status === 'proposed')) return 'Preparing'
  return 'Ready'
}

export function MissionRoute({ params, client = api }: RouteProps & { client?: Api }) {
  const missionID = params.missionId
  const detail = useStore((state) => (missionID ? state.missionDetails[missionID] : undefined))
  const missionRecords = useStore((state) => state.missions)
  const missions = useMemo(() => Object.values(missionRecords), [missionRecords])
  const setMissions = useStore((state) => state.setMissions)
  const setMissionDetail = useStore((state) => state.setMissionDetail)
  const setMissionLoading = useStore((state) => state.setMissionLoading)
  const setMissionError = useStore((state) => state.setMissionError)
  const missionLoading = useStore((state) => state.missionLoading)
  const missionError = useStore((state) => state.missionError)
  const missionNextCursor = useStore((state) =>
    state.missionListWorkspace === state.activeWorkspace ? state.missionNextCursor : null,
  )
  const workspaceID = useStore((state) => state.activeWorkspace)
  const cap = useCapability()
  const self = useSelf()
  const canLaunch = cap.hasMethod('mission.create') && allowed('launch', self)
  const navigate = useStore((state) => state.navigate)
  const [refresh, setRefresh] = useState(0)
  const [loadingMore, setLoadingMore] = useState(false)

  useEffect(() => {
    let live = true
    setMissionLoading(true)
    const load = async () => {
      try {
        if (!missionID) {
          const result = await client.missionList({ workspace_id: workspaceID, limit: 50 })
          if (live) setMissions(workspaceID, result.missions, result.next_cursor)
        } else {
          const result = await client.missionShow(missionID)
          if (live) {
            setMissionDetail({
              mission: result.mission,
              tasks: result.tasks,
              attempts: result.attempts ?? [],
              submissions: result.submissions ?? [],
              diagnostics: result.diagnostics ?? [],
              questions: result.questions ?? [],
              plan_reviews: result.plan_reviews ?? [],
            })
          }
        }
        if (live) setMissionLoading(false)
      } catch (error) {
        if (live) setMissionError(message(error))
      }
    }
    if (workspaceID || missionID) void load()
    else setMissionLoading(false)
    return () => {
      live = false
    }
  }, [client, missionID, refresh, setMissionDetail, setMissionError, setMissionLoading, setMissions, workspaceID])
  const loadMore = async () => {
    if (!workspaceID || !missionNextCursor || loadingMore) return
    setLoadingMore(true)
    try {
      const result = await client.missionList({
        workspace_id: workspaceID,
        limit: 50,
        before: missionNextCursor,
      })
      setMissions(workspaceID, result.missions, result.next_cursor, true)
    } catch (error) {
      setMissionError(message(error))
    } finally {
      setLoadingMore(false)
    }
  }


  if (missionID) {
    return (
      <MissionDetailView
        detail={detail}
        error={missionError}
        loading={missionLoading}
        onBack={() => navigate('missions')}
        onRefresh={() => setRefresh((value) => value + 1)}
        refreshGeneration={refresh}
        client={client}
      />
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ViewHeader
        title="Missions"
        subtitle="Bounded objectives coordinated by one integrator"
        actions={
          canLaunch ? (
            <Button size="sm" onClick={() => useStore.getState().openPaletteDialog('launch')}>
              Create swarm
            </Button>
          ) : undefined
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        {missionError && <ErrorNotice error={missionError} />}
        {missionLoading && !missions.length && <p className="text-sm text-muted-foreground">Loading missions…</p>}
        {!missionLoading && !missions.length && !missionError && (
          <div className="border border-dashed p-6 text-sm text-muted-foreground">
            <p className="font-medium text-foreground">No missions in this workspace.</p>
            <p className="mt-1">Launch a swarm when one objective needs bounded parallel work.</p>
          </div>
        )}
        <div className="grid gap-2">
          {missions
            .filter((mission) => !workspaceID || mission.workspace_id === workspaceID)
            .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
            .map((mission) => (
              <button
                key={mission.id}
                type="button"
                className="border bg-card p-3 text-left transition-colors hover:bg-toolbar-hover"
                onClick={() => navigate('missions', { missionId: mission.id })}
              >
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="break-words font-medium">{mission.objective}</p>
                    <p className="mt-1 font-mono text-[11px] text-muted-foreground">{mission.id}</p>
                  </div>
                  <PhaseChip mission={mission} />
                </div>
                <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <span>Integrator {mission.integrator.harness}</span>
                  <span>{mission.max_concurrent_attempts} concurrent</span>
                  <span>{mission.max_total_attempts} attempts total</span>
                  <span>Generation {mission.integrator_generation}</span>
                </div>
                {mission.integrator_launch_error && missionPhase(mission) !== 'rejected' && (
                  <p className="mt-1 break-words text-xs text-state-failed">
                    Integrator did not launch: {mission.integrator_launch_error}
                  </p>
                )}
              </button>
            ))}
        </div>
        {missionNextCursor && (
          <div className="mt-3 flex justify-center">
            <Button size="sm" variant="outline" onClick={() => void loadMore()} disabled={loadingMore}>
              {loadingMore ? 'Loading missions…' : 'Load older missions'}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}

function MissionDetailView({
  detail,
  error,
  loading,
  onBack,
  onRefresh,
  refreshGeneration,
  client,
}: {
  detail?: MissionShowResult
  error: string | null
  loading: boolean
  onBack: () => void
  onRefresh: () => void
  refreshGeneration: number
  client: Api
}) {
  const navigate = useStore((state) => state.navigate)
  const self = useSelf()
  const cap = useCapability()
  const [replaceOpen, setReplaceOpen] = useState(false)
  const [cancelOpen, setCancelOpen] = useState(false)
  const [releaseError, setReleaseError] = useState<string | null>(null)
  const [releasingAttemptID, setReleasingAttemptID] = useState<string | null>(null)
  const mission = detail?.mission
  const taskCounts = useMemo(() => {
    const counts: Record<string, number> = {}
    for (const task of detail?.tasks ?? []) counts[task.status] = (counts[task.status] ?? 0) + 1
    return counts
  }, [detail?.tasks])
  const integratorRunID = mission?.current_integrator_run_id
  const integratorRun = useStore((state) => (integratorRunID ? state.runs[integratorRunID] : undefined))
  const hydrated = useStore((state) => state.hydrated)
  const upsertRun = useStore((state) => state.upsertRun)
  const pending = usePendingApprovalRuns()
  // A run absent from the hydrated store is not proof it does not exist: a
  // create's response can outrun the run's first event. Only the server's
  // not-found marks it missing. Any other failure is asked again only on the
  // next Refresh, never in a loop.
  const [runLookups, setRunLookups] = useState<Record<string, 'checking' | 'found' | 'missing' | { error: string }>>({})
  const integratorLookup = hydrated && integratorRunID && !integratorRun ? runLookups[integratorRunID] : undefined
  const integratorPresent = Boolean(integratorRun)
  useEffect(() => {
    setRunLookups((current) => Object.fromEntries(Object.entries(current).filter(([, lookup]) => typeof lookup === 'string')))
  }, [refreshGeneration])
  useEffect(() => {
    if (!hydrated || !integratorRunID || integratorPresent || runLookups[integratorRunID]) return
    setRunLookups((current) => ({ ...current, [integratorRunID]: 'checking' }))
    client.runGet(integratorRunID).then((run) => {
      upsertRun(run)
      setRunLookups((current) => ({ ...current, [integratorRunID]: 'found' }))
    }).catch((err) => {
      const lookup = err instanceof ApiError && err.status === 404 ? 'missing' : { error: message(err) }
      setRunLookups((current) => ({ ...current, [integratorRunID]: lookup }))
    })
  }, [client, hydrated, integratorPresent, integratorRunID, runLookups, upsertRun])
  const acceptedSubmissions = useMemo(
    () => (detail?.submissions ?? []).filter((submission) => submission.state === 'accepted'),
    [detail?.submissions],
  )
  const canReplace =
    Boolean(mission) &&
    cap.hasMethod('mission.replace-integrator') &&
    allowed('launch', self)
  const canRelease =
    cap.hasMethod('mission.worker.release') &&
    allowed('launch', self)
  // The gate is the accountable human's own decision; an admin stands in.
  const gateAuthority =
    Boolean(mission) &&
    allowed('launch', self) &&
    (self.id === mission?.accountable_human_id || self.role === 'admin')
  const canAnswer = cap.hasMethod('mission.question.answer') && gateAuthority
  const canDecide = cap.hasMethod('mission.plan.decide') && gateAuthority
  const planReviews = detail?.plan_reviews ?? []
  const pendingReview = planReviews.find(
    (review) => review.plan_version === mission?.plan_version && !review.decision,
  )
  const latestFeedback = [...planReviews].reverse().find((review) => review.decision === 'revise')?.feedback
  const phase = mission ? missionPhase(mission) : 'active'
  // `amendment_review` keeps dispatching the approved set, so its attempts are
  // real; only the pending revisions in the round are frozen.
  const readOnly = phase !== 'active' && phase !== 'amendment_review'
  const canCancel =
    cap.hasMethod('mission.cancel') &&
    gateAuthority &&
    (phase === 'planning' || phase === 'clarified' || phase === 'plan_review')
  const releaseTakeover = async (attempt: MissionAttempt) => {
    if (!attempt.takeover_active || attempt.takeover_generation == null || !attempt.run_id || releasingAttemptID) return
    setReleaseError(null)
    setReleasingAttemptID(attempt.id)
    try {
      await client.missionWorkerRelease({
        run_id: attempt.run_id,
        expected_takeover_generation: attempt.takeover_generation,
      })
      toast.success('Human control released')
      onRefresh()
    } catch (error) {
      setReleaseError(message(error))
    } finally {
      setReleasingAttemptID(null)
    }
  }

  const taskList = detail ? (
    <>
      {detail.tasks.length === 0 && <p className="text-sm text-muted-foreground">The integrator has not proposed tasks yet.</p>}
      {detail.tasks.map((task) => (
        <TaskCard
          key={task.id}
          task={task}
          attempts={(detail.attempts ?? []).filter((attempt) => attempt.task_id === task.id)}
          submissions={(detail.submissions ?? []).filter((submission) => submission.task_id === task.id)}
          diagnostics={(detail.diagnostics ?? []).filter((diagnostic) => diagnostic.task_id === task.id)}
          readOnly={readOnly}
          showProposalBlocker={phase === 'active'}
          canRelease={canRelease}
          onRelease={releaseTakeover}
          releasingAttemptID={releasingAttemptID}
          onRun={(runID) => navigate('terminal', { runId: runID })}
        />
      ))}
    </>
  ) : null

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ViewHeader
        title={mission?.objective ?? 'Mission'}
        subtitle={mission ? `${missionStatus(mission, detail?.tasks ?? [], detail?.submissions ?? [])} · ${mission.id}` : undefined}
        actions={
          <>
            <Button size="sm" variant="outline" onClick={onBack}>
              All missions
            </Button>
            <Button size="sm" variant="outline" onClick={onRefresh} disabled={loading}>
              <RefreshCw className="size-3.5" aria-hidden />
              Refresh
            </Button>
            {canCancel && (
              <Button size="sm" variant="outline" onClick={() => setCancelOpen(true)}>
                Cancel swarm
              </Button>
            )}
          </>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        {error && <ErrorNotice error={error} />}
        {releaseError && <ErrorNotice error={releaseError} />}
        {typeof integratorLookup === 'object' && <ErrorNotice error={integratorLookup.error} />}
        {loading && !detail && <p className="text-sm text-muted-foreground">Loading mission…</p>}
        {mission && detail && (
          <>
            <PhaseBanner mission={mission} integratorRun={integratorRun} integratorMissing={integratorLookup === 'missing'} canReplace={canReplace} onReplace={() => setReplaceOpen(true)} />
            <section className="border bg-card p-3" aria-label="Mission authorization">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <p className="text-xs font-medium text-muted-foreground">Integrator</p>
                  <p className="mt-1 font-medium">
                    {mission.integrator.harness} · {mission.integrator.mode}
                  </p>
                  <p className="font-mono text-xs text-muted-foreground">
                    account {mission.integrator.account_member_id} · generation {mission.integrator_generation}
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-1">
                  {canReplace && (
                    <Button size="sm" variant="outline" onClick={() => setReplaceOpen(true)}>
                      Replace integrator
                    </Button>
                  )}
                  {integratorRun && (
                    <>
                      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                        <StatusChip state={runState(integratorRun.status, pending.has(integratorRun.id) || (integratorRun.unanswered_questions ?? 0) > 0)} />
                        {integratorRun.reason && <span className="break-words">{integratorRun.reason}</span>}
                      </span>
                      <Button size="sm" onClick={() => navigate('terminal', { runId: integratorRun.id })}>
                        Open integrator run
                      </Button>
                    </>
                  )}
                </div>
              </div>
              <div className="mt-3 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                <span>{mission.execution_choices.length} execution choices allowed</span>
                <span>Max {mission.max_concurrent_attempts} concurrent</span>
                <span>Max {mission.max_total_attempts} total attempts</span>
                <span>Accepted set {mission.accepted_set_version}</span>
              </div>
              <div className="mt-3 flex flex-wrap gap-1">
                {(['ready', 'working', 'review', 'done', 'abandoned', 'blocked'] as const).map((status) => (
                  <Chip key={status} color={status === 'done' ? 'success' : status === 'review' ? 'warning' : 'default'} variant="soft" size="sm">
                    <Chip.Label>{statusLabel[status]} {taskCounts[status] ?? 0}</Chip.Label>
                  </Chip>
                ))}
              </div>
            </section>

            {(phase === 'planning' || phase === 'clarified') && (
              <>
                <QuestionsSection
                  questions={detail.questions ?? []}
                  canAnswer={canAnswer && phase === 'planning'}
                  complete={phase === 'clarified'}
                  client={client}
                  onAnswered={onRefresh}
                />
                {latestFeedback && (
                  <div className="mt-3 border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-2 py-1.5 text-xs">
                    <p className="font-medium">Changes you requested</p>
                    <p className="mt-0.5 whitespace-pre-wrap break-words">{latestFeedback}</p>
                  </div>
                )}
                <section className="mt-3 space-y-2" aria-label="Draft plan">
                  <h2 className="text-sm font-semibold">Draft plan</h2>
                  {taskList}
                </section>
              </>
            )}
            {phase === 'plan_review' && (
              <PlanReviewSection
                mission={mission}
                review={pendingReview}
                canDecide={canDecide}
                client={client}
                onDecided={onRefresh}
              >
                {taskList}
              </PlanReviewSection>
            )}
            {phase === 'amendment_review' && (
              <AmendmentReviewSection
                mission={mission}
                review={pendingReview}
                tasks={detail.tasks}
                diagnostics={detail.diagnostics ?? []}
                canDecide={canDecide}
                client={client}
                onDecided={onRefresh}
              />
            )}
            {(phase === 'active' || phase === 'amendment_review' || phase === 'rejected') && (
              <section className="mt-3 space-y-2" aria-label="Mission tasks">
                <h2 className="text-sm font-semibold">Tasks</h2>
                {taskList}
              </section>
            )}
            {(phase === 'active' || phase === 'amendment_review') && (
              <section className="mt-3 border bg-card p-3" aria-label="Mission candidate review">
                <h2 className="text-sm font-semibold">Candidate progress</h2>
                <p className="mt-1 text-xs text-muted-foreground">Prepare and review the current accepted mission set through the existing verification and delivery workflow. Task status alone never implies delivery.</p>
                {integratorRunID ? (
                  <CandidateReview
                    workspaceID={mission.workspace_id}
                    currentRunID={integratorRunID}
                    missionID={mission.id}
                    missionSubmissions={acceptedSubmissions}
                    initialExpanded
                    client={client}
                  />
                ) : (
                  <p className="mt-2 text-xs text-muted-foreground">Waiting for the integrator run before candidate preparation can begin.</p>
                )}
              </section>
            )}
            {phase !== 'planning' && phase !== 'clarified' && (
              <PlanningHistory questions={detail.questions ?? []} reviews={planReviews} />
            )}
          </>
        )}
      </div>
      {replaceOpen && mission && (
        <IntegratorReplacement
          mission={mission}
          client={client}
          onClose={() => setReplaceOpen(false)}
          onReplaced={() => {
            setReplaceOpen(false)
            onRefresh()
          }}
        />
      )}
      {cancelOpen && mission && (
        <MissionCancel
          mission={mission}
          client={client}
          onClose={() => setCancelOpen(false)}
          onCancelled={() => {
            setCancelOpen(false)
            onRefresh()
          }}
        />
      )}
    </div>
  )
}

function TaskCard({
  task,
  attempts,
  submissions,
  diagnostics,
  readOnly,
  showProposalBlocker,
  canRelease,
  onRelease,
  releasingAttemptID,
  onRun,
}: {
  task: MissionTask
  attempts: MissionAttempt[]
  submissions: MissionSubmission[]
  diagnostics: MissionScopeDiagnostic[]
  /** Where the mission cannot dispatch, no attempt exists to chip. */
  readOnly: boolean
  /** Outside `active` a proposal is waiting on the human, which the phase
   * banner already says; repeating it as a blocker reads as a fault. */
  showProposalBlocker: boolean
  canRelease: boolean
  onRelease: (attempt: MissionAttempt) => void
  releasingAttemptID: string | null
  onRun: (runID: string) => void
}) {
  const revision = task.revision
  const pending = task.pending_revision
  const blockers = (task.blockers ?? []).filter((blocker) => showProposalBlocker || blocker.kind !== 'proposal')
  const accepted = submissions.find(
    (submission) =>
      submission.state === 'accepted' && submission.task_revision === task.current_revision,
  )
  return (
    <article className="border bg-card p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="break-words font-medium">{revision?.title || task.id}</p>
          <p className="mt-1 font-mono text-[11px] text-muted-foreground">
            {task.id} · revision {task.current_revision}
          </p>
        </div>
        <div className="flex flex-wrap gap-1">
          {pending && (
            <Chip color="warning" variant="soft" size="sm">
              <Chip.Label>{pending.material ? 'Material revision pending' : 'Revision pending'}</Chip.Label>
            </Chip>
          )}
          <Chip color={statusColor[task.status]} variant="soft" size="sm">
            <Chip.Label>{statusLabel[task.status]}</Chip.Label>
          </Chip>
        </div>
      </div>
      {pending && (
        <p className="mt-1 break-words text-xs text-muted-foreground">
          Revision {pending.revision}: {pending.title}
        </p>
      )}
      {revision && (
        <div className="mt-3 grid gap-2 text-xs sm:grid-cols-2">
          <div>
            <p className="font-medium text-muted-foreground">Objective</p>
            <p className="mt-0.5 whitespace-pre-wrap">{revision.objective}</p>
          </div>
          <div>
            <p className="font-medium text-muted-foreground">Scope</p>
            <ScopeFacts scope={revision.scope} />
          </div>
        </div>
      )}
      {blockers.length > 0 && (
        <div className="mt-3 border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-2 py-1.5 text-xs">
          <p className="font-medium">Blockers</p>
          {blockers.map((blocker, index) => (
            <p key={`${blocker.kind}-${index}`} className="mt-0.5">
              {blocker.kind}: {blocker.action} <span className="font-mono text-muted-foreground">({blocker.owner_run_id ?? 'unassigned'})</span>
            </p>
          ))}
        </div>
      )}
      {diagnostics.length > 0 && (
        <div className="mt-3 border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-2 py-1.5 text-xs">
          <p className="font-medium">Scope diagnostics</p>
          {diagnostics.map((diagnostic, index) => (
            <div key={`${diagnostic.kind}-${index}`} className="mt-1 flex flex-wrap items-center gap-1">
              <span>
                {diagnostic.kind.replaceAll('_', ' ')} · {diagnostic.unavailable
                  ? `Unavailable: ${diagnostic.unavailable_why || diagnostic.detail || 'snapshot or evidence is unavailable'}`
                  : diagnostic.paths.length > 0
                    ? diagnostic.paths.join(', ')
                    : diagnostic.detail || 'No paths reported'}
              </span>
              {diagnostic.run_id && <Button size="sm" variant="ghost" onClick={() => onRun(diagnostic.run_id)}>Task run</Button>}
              {diagnostic.peer_run_id && <Button size="sm" variant="ghost" onClick={() => onRun(diagnostic.peer_run_id!)}>Peer run</Button>}
            </div>
          ))}
        </div>
      )}
      {accepted && (
        <div className="mt-3 border-l-2 border-state-success bg-state-success/10 px-2 py-1.5 text-xs">
          <p className="font-medium">Accepted submission · revision {accepted.task_revision}</p>
          <p className="mt-0.5 font-mono break-all">{accepted.ref.run_id} · {accepted.ref.retained_revision}</p>
          {accepted.ref.evidence_ref && <p className="mt-0.5">Evidence: {accepted.ref.evidence_ref}</p>}
          {(accepted.scope_violations ?? []).length > 0 && <p className="mt-0.5 text-state-needs-attention">Scope violations: {accepted.scope_violations!.join(', ')}</p>}
          {accepted.acceptance?.scope_disposition && <p className="mt-0.5">Scope disposition: {accepted.acceptance.scope_disposition}</p>}
        </div>
      )}
      {!readOnly && <div className="mt-3 flex flex-wrap gap-1">
        {attempts.map((attempt) => (
          <div key={attempt.id} className="flex flex-wrap items-center gap-1 border px-2 py-1 text-xs">
            <span>Attempt {attempt.number} · {attempt.cancel_requested_at && !['completed', 'failed', 'cancelled', 'superseded', 'abandoned'].includes(attempt.state) ? 'Cancellation pending' : attempt.state}</span>
            <span className="font-mono text-muted-foreground">rev {attempt.task_revision}</span>
            {attempt.last_error && <span className="basis-full text-state-failed">{attempt.last_error}</span>}
            {(attempt.orchestration_hold || attempt.takeover_active) && <span className="text-state-needs-attention">Human control hold{attempt.takeover_member_id ? ` · ${attempt.takeover_member_id}` : ''}</span>}
            {attempt.run_id && <Button size="sm" variant="ghost" onClick={() => onRun(attempt.run_id)}>Worker run</Button>}
            {canRelease && attempt.takeover_active && <Button size="sm" variant="outline" disabled={releasingAttemptID === attempt.id} onClick={() => onRelease(attempt)}>{releasingAttemptID === attempt.id ? 'Releasing…' : 'Release control'}</Button>}
            {(attempt.orchestration_hold || attempt.takeover_active) && <span className="basis-full text-state-needs-attention">Release control here or from the worker run after expiry or restart.</span>}
          </div>
        ))}
      </div>}
    </article>
  )
}

function ScopeFacts({ scope }: { scope: MissionTaskScope }) {
  const facts = [
    ...(scope.expected_paths ?? []).map((path) => `Expected ${path}`),
    ...(scope.exclusions ?? []).map((path) => `Excluded ${path}`),
    ...(scope.semantic_responsibility ? [scope.semantic_responsibility] : []),
    ...(scope.target ? [`Target ${scope.target}`] : []),
  ]
  if (!facts.length) return <p className="mt-0.5 text-muted-foreground">No scope details reported.</p>
  return <ul className="mt-0.5 list-disc pl-4">{facts.map((fact) => <li key={fact}>{fact}</li>)}</ul>
}

function IntegratorReplacement({
  mission,
  client,
  onClose,
  onReplaced,
}: {
  mission: Mission
  client: Api
  onClose: () => void
  onReplaced: () => void
}) {
  const members = useStore((state) => state.members)
  // mission.replace-integrator accepts only an account and harness from the
  // mission's execution choices, of any mode, and runs it as tui.
  const choices = mission.execution_choices.filter(
    (choice, index, all) =>
      all.findIndex((other) => other.account_member_id === choice.account_member_id && other.harness === choice.harness) === index,
  )
  const accountIDs = [...new Set(choices.map((choice) => choice.account_member_id))]
  const current = choices.find(
    (choice) =>
      choice.account_member_id === mission.integrator.account_member_id &&
      choice.harness === mission.integrator.harness,
  ) ?? choices[0]
  const [account, setAccount] = useState(current?.account_member_id ?? '')
  const harnesses = choices.filter((choice) => choice.account_member_id === account).map((choice) => choice.harness)
  const [harness, setHarness] = useState(current?.harness ?? '')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const key = useRef(newIdempotencyKey())

  const chooseAccount = (next: string) => {
    setAccount(next)
    setHarness((value) =>
      choices.some((choice) => choice.account_member_id === next && choice.harness === value)
        ? value
        : choices.find((choice) => choice.account_member_id === next)?.harness ?? '',
    )
  }

  const submit = async () => {
    setSaving(true)
    setError(null)
    try {
      await client.missionReplaceIntegrator({
        mission_id: mission.id,
        expected_generation: mission.integrator_generation,
        integrator: { account_member_id: account, harness, mode: 'tui' },
        idempotency_key: key.current,
      })
      toast.success('Integrator replaced')
      onReplaced()
    } catch (err) {
      setSaving(false)
      setError(message(err))
    }
  }

  return (
    <div className="fixed inset-0 z-40 grid place-items-center bg-scrim p-4">
      <div role="dialog" aria-modal="true" aria-label="Replace integrator" className="w-full max-w-md border bg-popover p-4 shadow-overlay">
        <h2 className="text-base font-semibold">Replace integrator</h2>
        <p className="mt-1 text-xs text-muted-foreground">Pick an account and harness from this swarm&apos;s execution choices; the replacement runs interactive (tui). The current generation is pinned. Retry keeps the same idempotency key.</p>
        <div className="mt-3 grid gap-3">
          <div className="space-y-1.5"><Label>Account</Label><Select value={account} onValueChange={chooseAccount}><SelectTrigger><SelectValue placeholder="Choose an account" /></SelectTrigger><SelectContent>{accountIDs.map((id) => <SelectItem key={id} value={id}>{members[id]?.display_name ?? id}</SelectItem>)}</SelectContent></Select></div>
          <div className="space-y-1.5"><Label>Harness</Label><Select value={harness} onValueChange={setHarness}><SelectTrigger disabled={!harnesses.length}><SelectValue placeholder="Choose a harness" /></SelectTrigger><SelectContent>{harnesses.map((name) => <SelectItem key={name} value={name}>{name}</SelectItem>)}</SelectContent></Select></div>
        </div>
        {error && <ErrorNotice error={error} />}
        <div className="mt-4 flex justify-end gap-2"><Button variant="outline" onClick={onClose}>Cancel</Button><Button onClick={() => void submit()} disabled={saving || !account || !harness}>Replace</Button></div>
      </div>
    </div>
  )
}

/** One key per confirmation: a retry after a failure inside the same dialog
 * replays the cancel instead of issuing another. */
function MissionCancel({
  mission,
  client,
  onClose,
  onCancelled,
}: {
  mission: Mission
  client: Api
  onClose: () => void
  onCancelled: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const key = useRef(newIdempotencyKey())

  const cancel = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.missionCancel({ mission_id: mission.id, idempotency_key: key.current })
      toast.success('Swarm cancelled')
      onCancelled()
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <AlertDialog open onOpenChange={() => { if (!busy) onClose() }}>
      <AlertDialogContent className="max-h-[calc(100dvh-1rem)] sm:max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>Cancel this swarm?</AlertDialogTitle>
          <AlertDialogDescription>
            The mission moves to rejected, its integrator run is cancelled, and no worker starts. This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorNotice error={error} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Keep swarm</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void cancel()
            }}
          >
            Cancel swarm
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function MissionConflictDiagnostics({ run }: CardSlotProps) {
  const detailRecords = useStore((state) => state.missionDetails)
  const details = useMemo(() => Object.values(detailRecords), [detailRecords])
  const navigate = useStore((state) => state.navigate)
  const diagnostics = details
    .flatMap((detail) => detail.diagnostics ?? [])
    .filter((diagnostic) => diagnostic.run_id === run.id || diagnostic.peer_run_id === run.id)
  return diagnostics.map((diagnostic, index) => {
    const target =
      diagnostic.peer_run_id && diagnostic.peer_run_id !== run.id
        ? diagnostic.peer_run_id
        : diagnostic.run_id
    const label = diagnostic.kind.replaceAll('_', ' ')
    const detail = diagnostic.unavailable
      ? diagnostic.unavailable_why || diagnostic.detail || 'snapshot or evidence is unavailable'
      : diagnostic.detail || diagnostic.paths.join('\n') || 'No paths reported'
    return (
      <Button
        key={`${diagnostic.kind}-${diagnostic.task_id}-${index}`}
        type="button"
        variant="ghost"
        size="sm"
        className="h-[22px] min-h-[22px] border border-state-needs-attention/40 bg-state-needs-attention/10 px-1.5 text-[11px]"
        title={detail}
        disabled={!target}
        onClick={() => target && navigate('terminal', { runId: target })}
      >
        {label}{diagnostic.unavailable ? ' · unavailable' : ''}
        {!diagnostic.unavailable && diagnostic.paths.length
          ? ` · ${diagnostic.paths.length} path${diagnostic.paths.length === 1 ? '' : 's'}`
          : ''}
      </Button>
    )
  })
}

/** Compact mission marker contributed to ordinary run cards through the slot registry. */
function MissionRunChip({ run }: CardSlotProps) {
  const summaryRecords = useStore((state) => state.missions)
  const summaries = useMemo(() => Object.values(summaryRecords), [summaryRecords])
  const detailRecords = useStore((state) => state.missionDetails)
  const details = useMemo(() => Object.values(detailRecords), [detailRecords])
  const mission =
    [...details.map((detail) => detail.mission), ...summaries].find((candidate) =>
      candidate.current_integrator_run_id === run.id ||
      details.some((detail) =>
        detail.mission.id === candidate.id &&
        (detail.attempts ?? []).some((attempt) => attempt.run_id === run.id),
      ),
    )
  if (!mission) return null
  return <Chip color="accent" variant="soft" size="sm"><Chip.Label>Mission</Chip.Label></Chip>
}
registerSlot('card:chips', 'mission-diagnostics', MissionConflictDiagnostics)

registerSlot('card:badges', 'missions', MissionRunChip)
registerRoute('missions', MissionRoute)
