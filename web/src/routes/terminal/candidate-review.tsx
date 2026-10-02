import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { api, type Api } from '@/lib/api'
import type { EvidencePacket, MissionSubmission, Run } from '@/lib/types'
import type {
  Candidate,
  CandidateResolution,
  CandidateSummary,
  DeliveryAction,
  IntegrationPrepareParams,
  SubmissionRef,
  Verification,
} from '@/lib/integration-types'
import { useStore } from '@/store'
const maxPatchBytes = 1 << 20
const maxConflictResolutionBatch = 32
const defaultArgv = '[\"go\", \"test\", \"./...\"]'

type ResolutionDraft = { content: string; delete: boolean; selected: boolean }
type MutationOperation =
  | 'prepare'
  | 'resolve'
  | 'verify'
  | 'request_delivery'

function mutationDigest(operation: MutationOperation, params: unknown): string {
  if (params && typeof params === 'object' && 'idempotency_key' in params) {
    return `${operation}:${JSON.stringify({ ...(params as Record<string, unknown>), idempotency_key: '' })}`
  }
  return `${operation}:${JSON.stringify(params)}`
}

function idempotencyKey(operation: MutationOperation, params: unknown, keys: React.MutableRefObject<Map<string, string>>): string {
  const digest = mutationDigest(operation, params)
  const existing = keys.current.get(digest)
  if (existing) return existing
  const key = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${operation}-${Date.now()}-${Math.random().toString(16).slice(2)}`
  keys.current.set(digest, key)
  return key
}

function forgetIdempotencyKey(operation: MutationOperation, params: unknown, keys: React.MutableRefObject<Map<string, string>>) {
  keys.current.delete(mutationDigest(operation, params))
}
function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause)
}

function dateLabel(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}

function sourceLabel(packet: EvidencePacket): string {
  const sources = packet.sources ?? []
  if (!sources.length) return 'No source availability recorded'
  return sources
    .map((source) => `${source.name}: ${source.available ? 'available' : `unavailable${source.reason ? ` (${source.reason})` : ''}`}${source.truncated ? ', truncated' : ''}`)
    .join(' · ')
}

function packetSelectable(packet: EvidencePacket): boolean {
  const candidatePacket = packet as EvidencePacket & { availability?: string }
  if (!packet.retained_revision || candidatePacket.availability === 'expired') return false
  const git = packet.sources?.find((source) => source.name === 'git')
  return Boolean(git?.available && !git.truncated)
}

function acceptedMissionSubmissions(submissions: MissionSubmission[]): MissionSubmission[] {
  return submissions
    .filter((submission) => submission.state === 'accepted')
    .sort((left, right) => {
      const leftVersion = left.acceptance?.accepted_set_version ?? Number.MAX_SAFE_INTEGER
      const rightVersion = right.acceptance?.accepted_set_version ?? Number.MAX_SAFE_INTEGER
      return leftVersion - rightVersion || left.id.localeCompare(right.id)
    })
}

function submissionRef(submission: MissionSubmission): SubmissionRef {
  return {
    workspace_id: submission.ref.workspace_id,
    run_id: submission.ref.run_id,
    evidence_ref: submission.ref.evidence_ref,
    retained_revision: submission.ref.retained_revision,
  }
}

function missionAcceptedSetLabel(submissions: MissionSubmission[]): string {
  const latest = submissions.reduce((value, submission) => Math.max(value, submission.acceptance?.accepted_set_version ?? 0), 0)
  return latest > 0 ? `Accepted set ${latest}` : 'Accepted set unavailable'
}
function targetRevision(runs: Run[], currentRunID: string): string {
  return runs.find((run) => run.id === currentRunID && run.base_commit)?.base_commit
    || ''
}
export interface CandidateReviewProps {
  workspaceID: string
  currentRunID: string
  client?: Api
  /** Mission context reuses this review surface with the current accepted set. */
  missionID?: string
  missionSubmissions?: MissionSubmission[]
  initialExpanded?: boolean
}

export function CandidateReview({
  workspaceID,
  currentRunID,
  client = api,
  missionID,
  missionSubmissions = [],
  initialExpanded = false,
}: CandidateReviewProps) {
  const missionMode = Boolean(missionID)
  const [expanded, setExpanded] = useState(initialExpanded)
  const [browserOnline, setBrowserOnline] = useState(() => typeof navigator === 'undefined' || navigator.onLine)
  const gatewayConnection = useStore((state) => state.connection)
  const [authorityReady, setAuthorityReady] = useState(false)
  const [packets, setPackets] = useState<EvidencePacket[]>([])
  const [selectedIDs, setSelectedIDs] = useState<string[]>([])
  const [summaries, setSummaries] = useState<CandidateSummary[]>([])
  const [candidate, setCandidate] = useState<Candidate | null>(null)
  const [targetRef, setTargetRef] = useState('')
  const [expectedRevision, setExpectedRevision] = useState('')
  const [argvText, setArgvText] = useState(defaultArgv)
  const [timeoutSeconds, setTimeoutSeconds] = useState('300')
  const [deliveryAction, setDeliveryAction] = useState<DeliveryAction>('update_ref')
  const [selectedVerificationIDs, setSelectedVerificationIDs] = useState<string[]>([])
  const [resolutions, setResolutions] = useState<Record<string, ResolutionDraft>>({})
  const [patch, setPatch] = useState<{ text: string; truncated: boolean } | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState<MutationOperation | 'decide' | 'deliver' | null>(null)
  const [error, setError] = useState<string>()
  const [argvError, setArgvError] = useState<string>()
  const mutationKeys = useRef(new Map<string, string>())
  const loadGeneration = useRef(0)
  const previousConnection = useRef(gatewayConnection)
  const candidateVersion = useRef<{ id: string; version: number } | null>(null)
  const applyCandidate = useCallback((next: Candidate, preserveResolutionDrafts = false) => {
    const current = candidateVersion.current
    if (current && current.id === next.candidate_id && next.version < current.version) return false
    const changed = !current || current.id !== next.candidate_id || current.version !== next.version
    candidateVersion.current = { id: next.candidate_id, version: next.version }
    if (changed && !preserveResolutionDrafts) setResolutions({})
    setCandidate(next)
    return true
  }, [])

  const selectedPackets = useMemo(
    () => selectedIDs.map((id) => packets.find((packet) => packet.id === id)).filter((packet): packet is EvidencePacket => Boolean(packet)),
    [packets, selectedIDs],
  )
  const orderedMissionSubmissions = useMemo(
    () => acceptedMissionSubmissions(missionSubmissions),
    [missionSubmissions],
  )
  const selectedSubmissionRefs = useMemo(
    () => missionMode
      ? orderedMissionSubmissions.map(submissionRef)
      : selectedPackets.map((packet) => ({
        workspace_id: workspaceID,
        run_id: packet.run_id,
        evidence_ref: packet.id,
        retained_revision: packet.retained_revision || '',
      })),
    [missionMode, orderedMissionSubmissions, selectedPackets, workspaceID],
  )
  const request = candidate?.delivery_request
  const frozen = candidate?.state === 'frozen'
  const hasRunningVerification = candidate?.verifications.some((verification) => verification.status === 'running') ?? false
  const selectedPassing = candidate?.verifications.filter(
    (verification) => selectedVerificationIDs.includes(verification.verification_id)
      && verification.status === 'passed'
      && verification.candidate_revision === candidate.candidate_revision,
  ) ?? []
  const gatewayAvailable = browserOnline && gatewayConnection === 'live'
  const canMutate = gatewayAvailable && authorityReady && !busy

  const invalidateAuthority = useCallback(() => {
    loadGeneration.current += 1
    setAuthorityReady(false)
    setLoading(false)
    setBusy(null)
  }, [])

  const clearAuthority = useCallback(() => {
    invalidateAuthority()
    setPackets([])
    setSelectedIDs([])
    setSummaries([])
    candidateVersion.current = null
    setCandidate(null)
    setPatch(null)
    setResolutions({})
    setSelectedVerificationIDs([])
    setTargetRef('')
    setExpectedRevision('')
    setError(undefined)
    setArgvError(undefined)
  }, [invalidateAuthority])

  const loadWorkspacePackets = useCallback(async () => {
    if (!expanded) return
    const generation = ++loadGeneration.current
    setLoading(true)
    setError(undefined)
    try {
      const [nextWorkspace, listedRuns] = await Promise.all([
        client.workspaceGet(workspaceID),
        client.runList({ workspace_id: workspaceID }),
      ])
      let allRuns = listedRuns
      if (!allRuns.some((run) => run.id === currentRunID)) {
        const currentRun = await client.runGet(currentRunID)
        if (currentRun.id === currentRunID) allRuns = [...allRuns, currentRun]
      }
      const pages = await Promise.all(allRuns.map((run) => client.runEvidenceList({
        workspace_id: workspaceID,
        run_id: run.id,
        limit: 100,
      })))
      if (generation !== loadGeneration.current) return
      const byID = new Map<string, EvidencePacket>()
      pages.forEach((page) => page.packets.forEach((packet) => byID.set(packet.id, packet)))
      setPackets(Array.from(byID.values()))
      setTargetRef((value) => value || `refs/heads/${nextWorkspace.base_branch}`)
      setExpectedRevision((value) => value || targetRevision(allRuns, currentRunID))
      const listedCandidates = await client.integrationList({ workspace_id: workspaceID, limit: 50 })
      const selectedCandidateID = candidateVersion.current?.id
      const refreshedCandidate = selectedCandidateID
        ? await client.integrationShow({ workspace_id: workspaceID, candidate_id: selectedCandidateID })
        : null
      if (generation === loadGeneration.current) {
        setSummaries(listedCandidates.candidates)
        if (refreshedCandidate) applyCandidate(refreshedCandidate.candidate)
        setAuthorityReady(true)
      }
    } catch (cause) {
      if (generation === loadGeneration.current) {
        setAuthorityReady(false)
        setError(message(cause))
      }
    } finally {
      if (generation === loadGeneration.current) setLoading(false)
    }
  }, [applyCandidate, client, currentRunID, expanded, workspaceID])

  useEffect(() => {
    if (!expanded) return
    clearAuthority()
    void loadWorkspacePackets()
  }, [clearAuthority, expanded, loadWorkspacePackets])

  useEffect(() => {
    if (previousConnection.current === gatewayConnection) return
    previousConnection.current = gatewayConnection
    if (gatewayConnection !== 'live') {
      invalidateAuthority()
      return
    }
    // The stream's live acknowledgement is also a recovery signal when a
    // browser online event races the socket reconnect. Resync from the
    // browser fact first; a stale live event must not unlock an offline tab.
    setBrowserOnline(typeof navigator === 'undefined' || navigator.onLine)
    if (expanded) {
      invalidateAuthority()
      void loadWorkspacePackets()
    }
  }, [expanded, gatewayConnection, invalidateAuthority, loadWorkspacePackets])

  useEffect(() => {
    const becameOnline = () => {
      setBrowserOnline(true)
      if (gatewayConnection === 'live' && expanded) {
        invalidateAuthority()
        void loadWorkspacePackets()
      }
    }
    const becameOffline = () => {
      setBrowserOnline(false)
      invalidateAuthority()
    }
    window.addEventListener('online', becameOnline)
    window.addEventListener('offline', becameOffline)
    return () => {
      window.removeEventListener('online', becameOnline)
      window.removeEventListener('offline', becameOffline)
    }
  }, [expanded, gatewayConnection, invalidateAuthority, loadWorkspacePackets])

  useEffect(() => {
    if (!candidate || !hasRunningVerification || !expanded || !gatewayAvailable) return
    const generation = loadGeneration.current
    const candidateID = candidate.candidate_id
    let cancelled = false
    let inFlight = false
    const poll = async () => {
      if (cancelled || inFlight || loadGeneration.current !== generation || !gatewayAvailable) return
      inFlight = true
      try {
        const result = await client.integrationShow({ workspace_id: workspaceID, candidate_id: candidateID })
        if (!cancelled && loadGeneration.current === generation && gatewayAvailable) applyCandidate(result.candidate)
      } catch (cause) {
        if (!cancelled && loadGeneration.current === generation) setError(message(cause))
      } finally {
        inFlight = false
      }
    }
    const timer = window.setInterval(() => void poll(), 2000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [applyCandidate, candidate, client, expanded, gatewayAvailable, hasRunningVerification, workspaceID])

  const selectPacket = (packet: EvidencePacket) => {
    if (!packetSelectable(packet)) return
    setSelectedIDs((current) => current.includes(packet.id)
      ? current.filter((id) => id !== packet.id)
      : [...current, packet.id])
  }

  const reorder = (index: number, direction: -1 | 1) => {
    const next = index + direction
    if (next < 0 || next >= selectedIDs.length) return
    setSelectedIDs((current) => {
      const values = [...current]
      ;[values[index], values[next]] = [values[next], values[index]]
      return values
    })
  }

  const prepare = async () => {
    const minimumInputs = missionMode ? 1 : 2
    if (!canMutate || selectedSubmissionRefs.length < minimumInputs || !targetRef || !expectedRevision) return
    const params: IntegrationPrepareParams = {
      workspace_id: workspaceID,
      ...(missionID ? { mission_id: missionID } : {}),
      submissions: selectedSubmissionRefs,
      target_ref: targetRef,
      expected_target_revision: expectedRevision,
      idempotency_key: '',
    }
    params.idempotency_key = idempotencyKey('prepare', params, mutationKeys)
    const generation = loadGeneration.current
    setBusy('prepare')
    setError(undefined)
    try {
      const result = await client.integrationPrepare(params)
      forgetIdempotencyKey('prepare', params, mutationKeys)
      if (generation !== loadGeneration.current) return
      if (!applyCandidate(result.candidate)) return
      setPatch(null)
      setSummaries((current) => [result.candidate, ...current.filter((item) => item.candidate_id !== result.candidate.candidate_id)])
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setBusy(null)
    }
  }
  const showCandidate = async (candidateID: string) => {
    if (loading || !gatewayAvailable || busy) return
    loadGeneration.current += 1
    const generation = loadGeneration.current
    setAuthorityReady(false)
    candidateVersion.current = null
    setCandidate(null)
    setPatch(null)
    setSelectedVerificationIDs([])
    setResolutions({})
    setLoading(true)
    setError(undefined)
    try {
      const result = await client.integrationShow({ workspace_id: workspaceID, candidate_id: candidateID })
      if (generation !== loadGeneration.current) return
      if (!applyCandidate(result.candidate)) {
        setAuthorityReady(true)
        return
      }
      setAuthorityReady(true)
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setLoading(false)
    }
  }

  const loadPatch = async () => {
    if (!candidate || candidate.state !== 'frozen' || patch || !gatewayAvailable) return
    const generation = loadGeneration.current
    const candidateID = candidate.candidate_id
    setLoading(true)
    setError(undefined)
    try {
      const result = await client.integrationPatch({ workspace_id: workspaceID, candidate_id: candidateID })
      if (generation !== loadGeneration.current) return
      setPatch({ text: result.patch.slice(0, maxPatchBytes), truncated: result.truncated || result.patch.length > maxPatchBytes })
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setLoading(false)
    }
  }
  const resolveConflicts = async () => {
    if (!canMutate || !candidate || candidate.state !== 'conflicted') return
    const submittedPaths = (candidate.conflicts ?? []).filter((path) => resolutions[path]?.selected)
    if (!submittedPaths.length) {
      setError('Select at least one file resolution before applying.')
      return
    }
    if (submittedPaths.length > maxConflictResolutionBatch) {
      setError(`Apply at most ${maxConflictResolutionBatch} file resolutions at a time; untouched drafts remain available.`)
      return
    }
    const files: CandidateResolution[] = submittedPaths.map((path) => {
      const draft = resolutions[path]!
      return { path, content: draft.delete ? undefined : draft.content, delete: draft.delete }
    })
    const params = { workspace_id: workspaceID, candidate_id: candidate.candidate_id, expected_version: candidate.version, files, idempotency_key: '' }
    params.idempotency_key = idempotencyKey('resolve', params, mutationKeys)
    const generation = loadGeneration.current
    setBusy('resolve')
    setError(undefined)
    try {
      const result = await client.integrationResolve(params)
      forgetIdempotencyKey('resolve', params, mutationKeys)
      if (generation !== loadGeneration.current) return
      const preserveUntouchedDrafts = candidate.candidate_id === result.candidate.candidate_id
        && candidate.applied_inputs === result.candidate.applied_inputs
      if (!applyCandidate(result.candidate, preserveUntouchedDrafts)) return
      setResolutions((current) => {
        if (!preserveUntouchedDrafts) return {}
        const conflicts = new Set(result.candidate.conflicts ?? [])
        const next: Record<string, ResolutionDraft> = {}
        Object.entries(current).forEach(([path, draft]) => {
          if (!submittedPaths.includes(path) && conflicts.has(path)) next[path] = draft
        })
        return next
      })
      setPatch(null)
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setBusy(null)
    }
  }

  const runVerification = async () => {
    if (!canMutate || !candidate?.candidate_revision || !frozen) return
    let parsedArgv: unknown
    try {
      parsedArgv = JSON.parse(argvText)
    } catch {
      setArgvError('Verification argv must be valid JSON.')
      return
    }
    if (!Array.isArray(parsedArgv) || parsedArgv.length === 0 || parsedArgv.some((value) => typeof value !== 'string')) {
      setArgvError('Verification argv must be a non-empty JSON array of strings.')
      return
    }
    const argv = parsedArgv as string[]
    const timeout = Number(timeoutSeconds)
    if (!Number.isInteger(timeout) || timeout <= 0) {
      setArgvError('Timeout seconds must be a positive whole number.')
      return
    }
    setArgvError(undefined)
    const params = {
      workspace_id: workspaceID,
      candidate_id: candidate.candidate_id,
      candidate_revision: candidate.candidate_revision,
      argv,
      timeout_seconds: timeout,
      idempotency_key: '',
    }
    params.idempotency_key = idempotencyKey('verify', params, mutationKeys)
    const generation = loadGeneration.current
    setBusy('verify')
    setError(undefined)
    try {
      const result = await client.integrationVerify(params)
      forgetIdempotencyKey('verify', params, mutationKeys)
      if (generation !== loadGeneration.current) return
      if (!applyCandidate(result.candidate)) return
      setSelectedVerificationIDs([])
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setBusy(null)
    }
  }
  const requestDelivery = async () => {
    if (!canMutate || !candidate?.candidate_revision || !frozen || !selectedPassing.length) return
    const params = {
      workspace_id: workspaceID,
      candidate_id: candidate.candidate_id,
      candidate_revision: candidate.candidate_revision,
      verification_ids: selectedPassing.map((verification) => verification.verification_id),
      action: deliveryAction,
      idempotency_key: '',
    }
    params.idempotency_key = idempotencyKey('request_delivery', params, mutationKeys)
    const generation = loadGeneration.current
    setBusy('request_delivery')
    try {
      const result = await client.integrationRequestDelivery(params)
      forgetIdempotencyKey('request_delivery', params, mutationKeys)
      if (generation !== loadGeneration.current) return
      if (!applyCandidate(result.candidate)) return
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setBusy(null)
    }
  }

  const decide = async (approve: boolean) => {
    if (!canMutate || !candidate || !request || request.state !== 'pending') return
    const generation = loadGeneration.current
    const params = {
      workspace_id: workspaceID,
      candidate_id: candidate.candidate_id,
      request_id: request.request_id,
      request_version: request.request_version,
      approve,
    }
    setBusy('decide')
    try {
      const result = await client.integrationDecide(params)
      if (generation !== loadGeneration.current) return
      if (!applyCandidate(result.candidate)) return
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setBusy(null)
    }
  }

  const deliver = async () => {
    if (!canMutate || !candidate || !request || request.state !== 'approved') return
    const generation = loadGeneration.current
    const params = {
      workspace_id: workspaceID,
      candidate_id: candidate.candidate_id,
      request_id: request.request_id,
      request_version: request.request_version,
    }
    setBusy('deliver')
    try {
      const result = await client.integrationDeliver(params)
      if (generation !== loadGeneration.current) return
      if (!applyCandidate(result.candidate)) return
    } catch (cause) {
      if (generation === loadGeneration.current) setError(message(cause))
    } finally {
      if (generation === loadGeneration.current) setBusy(null)
    }
  }

  return (
    <div className="mt-3 border-t border-border pt-3">
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-expanded={expanded}
        aria-controls="candidate-review-region"
        onClick={() => setExpanded((value) => !value)}
      >
        Candidate review
      </Button>
      {expanded && (
        <div id="candidate-review-region" className="mt-2 space-y-3" role="region" aria-label="Candidate review">
          {(!gatewayAvailable || !authorityReady) && <p role="status" className="border border-state-attention/40 bg-state-attention/10 p-2 text-[11px] text-state-attention">{!gatewayAvailable ? 'Offline or reconnecting.' : 'Loading fresh workspace authority.'} Mutation controls remain disabled until the gateway reconnects and fresh authority is loaded.</p>}
          {error && <div role="alert" className="flex items-start justify-between gap-2 border border-state-failed/30 bg-state-failed/10 p-2 text-[11px] text-state-failed"><span>{error}</span>{candidate && <Button type="button" size="sm" variant="ghost" disabled={loading || !gatewayAvailable} onClick={() => void showCandidate(candidate.candidate_id)}>Reload candidate</Button>}</div>}
          {missionMode ? (
            <div className="space-y-2" data-testid="mission-candidate-inputs">
              <h3 className="text-[12px] font-semibold">Mission accepted inputs</h3>
              <p className="text-[11px] text-muted-foreground">The current accepted set is sent exactly as recorded by the mission. Order follows acceptance version; refresh and retry if the set changed.</p>
              <p className="text-[11px] text-muted-foreground">{missionAcceptedSetLabel(orderedMissionSubmissions)} · {orderedMissionSubmissions.length} input{orderedMissionSubmissions.length === 1 ? '' : 's'}</p>
              {orderedMissionSubmissions.length === 0 ? (
                <p className="border border-state-attention/40 bg-state-attention/10 p-2 text-[11px] text-state-attention">No current accepted submissions are available for preparation.</p>
              ) : (
                <ol className="space-y-1 border border-border p-2 text-[11px]" aria-label="Ordered mission candidate inputs">
                  {orderedMissionSubmissions.map((submission, index) => (
                    <li key={submission.id} className="flex items-center gap-1">
                      <span className="w-4 text-muted-foreground">{index + 1}.</span>
                      <span className="min-w-0 flex-1">
                        <code className="block truncate">{submission.ref.evidence_ref}</code>
                        <span className="block truncate text-muted-foreground">run {submission.ref.run_id} · retained <code>{submission.ref.retained_revision}</code></span>
                      </span>
                      {submission.acceptance?.accepted_set_version != null && <span className="shrink-0 text-muted-foreground">#{submission.acceptance.accepted_set_version}</span>}
                    </li>
                  ))}
                </ol>
              )}
            </div>
          ) : (
            <div className="space-y-2">
              <h3 className="text-[12px] font-semibold">Select retained packets</h3>
              <p className="text-[11px] text-muted-foreground">Select at least two precise packets. Retained revisions are immutable; unavailable sources cannot be prepared.</p>
              {loading && !packets.length && <p className="text-[11px] text-muted-foreground">Loading workspace evidence…</p>}
              <div className="divide-y divide-border border border-border">
                {packets.map((packet) => {
                  const selectable = packetSelectable(packet)
                  return (
                    <label key={packet.id} className={`block p-2 text-[11px] ${selectable ? 'cursor-pointer' : 'opacity-60'}`}>
                      <span className="flex items-start gap-2">
                        <input type="checkbox" aria-label={`Select packet ${packet.id}`} checked={selectedIDs.includes(packet.id)} disabled={!selectable} onChange={() => selectPacket(packet)} />
                        <span className="min-w-0 flex-1">
                          <span className="flex justify-between gap-2"><strong className="truncate">{packet.trigger} · {packet.id}</strong><time className="shrink-0 text-muted-foreground">{dateLabel(packet.captured_at)}</time></span>
                          <span className="block truncate text-muted-foreground">{packet.objective || 'Objective not recorded'}</span>
                          <span className="mt-1 block text-muted-foreground">Retained revision: <code>{packet.retained_revision || 'Unavailable'}</code></span>
                          <span className="block text-muted-foreground">Source status: {sourceLabel(packet)}</span>
                          <span className="block text-muted-foreground">Packet availability: {(packet as EvidencePacket & { availability?: string }).availability || 'legacy packet (source status above)'}</span>
                          <span className="block text-muted-foreground">Claimed: {packet.provenance || 'Not recorded'} · Observed: {packet.retained_revision ? `Git revision ${packet.retained_revision}` : 'Unavailable'}</span>
                        </span>
                      </span>
                    </label>
                  )
                })}
              </div>
              {selectedPackets.length > 0 && <ol className="space-y-1 border border-border p-2 text-[11px]" aria-label="Ordered candidate inputs">
                {selectedPackets.map((packet, index) => <li key={packet.id} className="flex items-center gap-1"><span className="w-4 text-muted-foreground">{index + 1}.</span><code className="min-w-0 flex-1 truncate">{packet.id}</code><Button type="button" size="sm" variant="ghost" aria-label={`Move packet ${packet.id} up`} disabled={index === 0} onClick={() => reorder(index, -1)}>↑</Button><Button type="button" size="sm" variant="ghost" aria-label={`Move packet ${packet.id} down`} disabled={index === selectedPackets.length - 1} onClick={() => reorder(index, 1)}>↓</Button><Button type="button" size="sm" variant="ghost" aria-label={`Remove packet ${packet.id}`} onClick={() => selectPacket(packet)}>Remove</Button></li>)}
              </ol>}
            </div>
          )}

          <div className="grid gap-2 sm:grid-cols-2">
            <div className="space-y-1"><Label htmlFor="candidate-target-ref">Target ref</Label><Input id="candidate-target-ref" value={targetRef} onChange={(event) => setTargetRef(event.target.value)} placeholder="refs/heads/main" disabled={Boolean(candidate)} /></div>
            <div className="space-y-1"><Label htmlFor="candidate-target-revision">Expected target revision</Label><Input id="candidate-target-revision" value={expectedRevision} onChange={(event) => setExpectedRevision(event.target.value)} placeholder="Authoritative workspace commit" disabled={Boolean(candidate)} /><p className="text-[10px] text-muted-foreground">Read from the workspace run's captured base commit; never guessed.</p></div>
          </div>
          {summaries.length > 0 && <div className="space-y-1"><h3 className="text-[12px] font-semibold">Existing candidates</h3><div className="divide-y divide-border border border-border">{summaries.map((item) => <div key={item.candidate_id} className="flex items-center gap-2 p-2 text-[11px]"><span className="min-w-0 flex-1"><code className="block truncate">{item.candidate_id}</code><span className="text-muted-foreground">{item.state} · {item.candidate_revision || 'No combined revision'}</span></span><Button type="button" size="sm" variant="outline" disabled={loading || Boolean(busy) || !gatewayAvailable} onClick={() => void showCandidate(item.candidate_id)}>Show full</Button></div>)}</div></div>}
          <Button type="button" size="sm" disabled={!canMutate || selectedSubmissionRefs.length < (missionMode ? 1 : 2) || !targetRef || !expectedRevision || Boolean(candidate)} onClick={() => void prepare()}>Prepare candidate</Button>
          {candidate && <CandidateDetails
            candidate={candidate}
            patch={patch}
            resolutions={resolutions}
            setResolutions={setResolutions}
            onLoadPatch={() => void loadPatch()}
            onResolve={() => void resolveConflicts()}
            onRunVerification={() => void runVerification()}
            argvText={argvText}
            setArgvText={setArgvText}
            timeoutSeconds={timeoutSeconds}
            setTimeoutSeconds={setTimeoutSeconds}
            argvError={argvError}
            selectedVerificationIDs={selectedVerificationIDs}
            setSelectedVerificationIDs={setSelectedVerificationIDs}
            selectedPassing={selectedPassing}
            deliveryAction={deliveryAction}
            setDeliveryAction={setDeliveryAction}
            onRequestDelivery={() => void requestDelivery()}
            onDecide={(approve) => void decide(approve)}
            onDeliver={() => void deliver()}
            canMutate={canMutate}
            busy={busy}
          />}
        </div>
      )}
    </div>
  )
}

interface CandidateDetailsProps {
  candidate: Candidate
  patch: { text: string; truncated: boolean } | null
  resolutions: Record<string, ResolutionDraft>
  setResolutions: React.Dispatch<React.SetStateAction<Record<string, ResolutionDraft>>>
  onLoadPatch: () => void
  onResolve: () => void
  onRunVerification: () => void
  argvText: string
  setArgvText: (value: string) => void
  timeoutSeconds: string
  setTimeoutSeconds: (value: string) => void
  argvError?: string
  selectedVerificationIDs: string[]
  setSelectedVerificationIDs: React.Dispatch<React.SetStateAction<string[]>>
  selectedPassing: Verification[]
  deliveryAction: DeliveryAction
  setDeliveryAction: (value: DeliveryAction) => void
  onRequestDelivery: () => void
  onDecide: (approve: boolean) => void
  onDeliver: () => void
  canMutate: boolean
  busy: MutationOperation | 'decide' | 'deliver' | null
}

function CandidateDetails(props: CandidateDetailsProps) {
  const { candidate, patch, resolutions, setResolutions, onLoadPatch, onResolve, onRunVerification, argvText, setArgvText, timeoutSeconds, setTimeoutSeconds, argvError, selectedVerificationIDs, setSelectedVerificationIDs, selectedPassing, deliveryAction, setDeliveryAction, onRequestDelivery, onDecide, onDeliver, canMutate, busy } = props
  const request = candidate.delivery_request
  const selectedResolutionCount = Object.values(resolutions).filter((draft) => draft.selected).length
  return (
    <div className="space-y-3 border-t border-border pt-3">
      <div className="flex items-start justify-between gap-2"><div><h3 className="text-[12px] font-semibold">Candidate details</h3><p className="text-[11px] text-muted-foreground">State: <strong>{candidate.state}</strong> · Applied inputs: {candidate.applied_inputs}/{candidate.inputs.length}</p></div><code className="max-w-[55%] truncate text-[11px]">{candidate.candidate_revision || 'No combined revision'}</code></div>
      {candidate.mission_id && <p className="text-[11px] text-muted-foreground">Mission <code>{candidate.mission_id}</code>{candidate.mission_accepted_set_version != null && <> · frozen accepted set <strong>{candidate.mission_accepted_set_version}</strong></>}</p>}
      {candidate.error && <p role="alert" className="text-[11px] text-state-failed">Blocked: {candidate.error}</p>}
      <dl className="grid gap-1 text-[11px] text-muted-foreground sm:grid-cols-2"><div><dt className="inline font-medium">Target ref: </dt><dd className="inline break-all">{candidate.target_ref}</dd></div><div><dt className="inline font-medium">Expected target revision: </dt><dd className="inline break-all">{candidate.expected_target_revision}</dd></div></dl>
      <div><h4 className="text-[11px] font-semibold">Ordered inputs</h4><ol className="mt-1 space-y-1 text-[11px] text-muted-foreground">{candidate.inputs.map((input, index) => <li key={`${input.submission.evidence_ref}-${index}`}>{index + 1}. <code>{input.submission.evidence_ref}</code> · retained <code>{input.submission.retained_revision}</code> · base <code>{input.base_revision}</code></li>)}</ol></div>
      {candidate.state === 'conflicted' && <div className="space-y-2 border border-state-attention/40 bg-state-attention/5 p-2"><h4 className="text-[11px] font-semibold">Conflicts</h4><p className="text-[10px] text-muted-foreground">Enter complete resolved file contents. Select only files to apply; untouched drafts remain conflicted and are preserved.</p><p className="text-[10px] text-muted-foreground">Selected resolutions: {selectedResolutionCount}/{maxConflictResolutionBatch}{selectedResolutionCount > maxConflictResolutionBatch ? ' (apply in partial batches)' : ''}</p>{(candidate.conflicts ?? []).map((path) => { const draft = resolutions[path] ?? { content: '', delete: false, selected: false }; return <div key={path} className="space-y-1"><Label htmlFor={`candidate-resolution-${path}`}>{path} · complete file content</Label><Textarea id={`candidate-resolution-${path}`} aria-label={`Resolution for ${path}`} value={draft.content} disabled={draft.delete || !canMutate} onChange={(event) => setResolutions((current) => ({ ...current, [path]: { ...draft, content: event.target.value, selected: true } }))} placeholder="Complete resolved file contents" /><label className="flex items-center gap-2 text-[11px]"><input type="checkbox" aria-label={`Apply resolution for ${path}`} checked={draft.selected} disabled={!canMutate} onChange={(event) => setResolutions((current) => ({ ...current, [path]: { ...draft, selected: event.target.checked } }))} />Apply this resolution</label><label className="flex items-center gap-2 text-[11px]"><input type="checkbox" checked={draft.delete} disabled={!canMutate} onChange={(event) => setResolutions((current) => ({ ...current, [path]: { ...draft, delete: event.target.checked, selected: event.target.checked || draft.selected } }))} />Delete file</label></div> })}<Button type="button" size="sm" disabled={!canMutate || busy === 'resolve' || selectedResolutionCount === 0 || selectedResolutionCount > maxConflictResolutionBatch} onClick={onResolve}>Apply resolutions</Button></div>}
      {candidate.state === 'frozen' && <div className="space-y-2"><p className="text-[11px] text-state-success">Frozen at exact combined revision <code>{candidate.candidate_revision}</code>.</p><Button type="button" variant="outline" size="sm" disabled={busy !== null || Boolean(patch)} onClick={onLoadPatch}>Load combined patch</Button>{patch && <div><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words border border-border bg-muted/20 p-2 font-mono text-[10px]">{patch.text || 'Combined patch is empty.'}</pre>{patch.truncated && <p className="mt-1 text-[10px] text-muted-foreground">Patch truncated at the 1 MiB server bound.</p>}</div>}</div>}
      <div className="space-y-2 border-t border-border pt-2"><h4 className="text-[11px] font-semibold">Verification</h4><div className="grid gap-2 sm:grid-cols-[1fr_7rem]"><div className="space-y-1"><Label htmlFor="candidate-verification-argv">Verification argv</Label><Textarea id="candidate-verification-argv" value={argvText} onChange={(event) => setArgvText(event.target.value)} disabled={!canMutate || !candidate.candidate_revision || candidate.state !== 'frozen'} aria-invalid={Boolean(argvError)} /></div><div className="space-y-1"><Label htmlFor="candidate-verification-timeout">Timeout seconds</Label><Input id="candidate-verification-timeout" type="number" min={1} value={timeoutSeconds} onChange={(event) => setTimeoutSeconds(event.target.value)} disabled={!canMutate || !candidate.candidate_revision || candidate.state !== 'frozen'} /></div></div>{argvError && <p role="alert" className="text-[11px] text-state-failed">{argvError}</p>}<Button type="button" size="sm" disabled={!canMutate || candidate.state !== 'frozen' || !candidate.candidate_revision || busy === 'verify'} onClick={onRunVerification}>Run verification</Button>{candidate.verifications.map((verification) => <VerificationRow key={verification.verification_id} verification={verification} selected={selectedVerificationIDs.includes(verification.verification_id)} onSelect={() => setSelectedVerificationIDs((current) => current.includes(verification.verification_id) ? current.filter((id) => id !== verification.verification_id) : [...current, verification.verification_id])} />)}</div>
      <div className="space-y-2 border-t border-border pt-2"><h4 className="text-[11px] font-semibold">Delivery</h4>{request ? <div className="space-y-2 text-[11px]"><p>Request <code>{request.request_id}</code> · immutable request version <strong>{request.request_version}</strong> · {request.state}</p><p>Checks: {request.verification_ids.join(', ') || 'none'} · action: {request.action}</p>{request.state === 'pending' && <div className="flex flex-wrap gap-1"><Button type="button" size="sm" disabled={!canMutate || busy === 'decide'} onClick={() => onDecide(true)}>Approve delivery</Button><Button type="button" size="sm" variant="outline" disabled={!canMutate || busy === 'decide'} onClick={() => onDecide(false)}>Deny delivery</Button></div>}{request.state === 'approved' && <Button type="button" size="sm" disabled={!canMutate || busy === 'deliver'} onClick={onDeliver}>Deliver candidate</Button>}</div> : <><div className="space-y-1"><Label htmlFor="candidate-delivery-action">Delivery action</Label><select id="candidate-delivery-action" className="h-[26px] w-full rounded-[2px] border border-input bg-background px-2 text-[12px]" value={deliveryAction} onChange={(event) => setDeliveryAction(event.target.value as DeliveryAction)} disabled={!canMutate}><option value="update_ref">Update target ref</option><option value="proposal">Create proposal</option></select></div><p className="text-[11px] text-muted-foreground">Only currently selected passing verification IDs are requested; failed checks never imply pass.</p><Button type="button" size="sm" disabled={!canMutate || candidate.state !== 'frozen' || !candidate.candidate_revision || !selectedPassing.length} onClick={onRequestDelivery}>Request delivery</Button></>}</div>
      {candidate.delivery_receipt && <div className="border border-state-success/30 bg-state-success/5 p-2 text-[11px]"><strong>Receipt: {candidate.delivery_receipt.result === 'landed' ? 'Landed' : 'Proposed'}</strong><p>Target {candidate.delivery_receipt.target_ref} · revision <code>{candidate.delivery_receipt.candidate_revision}</code>{candidate.delivery_receipt.proposal_ref ? ` · ${candidate.delivery_receipt.proposal_ref}` : ''}</p></div>}
    </div>
  )
}

function VerificationRow({ verification, selected, onSelect }: { verification: Verification; selected: boolean; onSelect: () => void }) {
  const observed = [
    verification.observed_image && `image ${verification.observed_image}`,
    verification.user && `user ${verification.user}`,
    verification.working_dir && `path ${verification.working_dir}`,
    verification.cpu_limit !== undefined && `cpu ${verification.cpu_limit}`,
    verification.memory_limit_bytes !== undefined && `memory ${verification.memory_limit_bytes}`,
    verification.environment_sha256 && `environment ${verification.environment_sha256}`,
    verification.setup_script_sha256 && `setup ${verification.setup_script_sha256}`,
  ].filter(Boolean).join(' · ')
  return <div className="border border-border p-2 text-[10px]"><div className="flex items-start gap-2"><input type="checkbox" aria-label={`Select verification ${verification.verification_id}`} checked={selected} disabled={verification.status !== 'passed'} onChange={onSelect} /><span className="min-w-0 flex-1"><strong>{verification.status}</strong> · <code>{verification.verification_id}</code> · exit {verification.exit_code ?? '—'}<span className="block text-muted-foreground">Observed: {observed || 'Not reported'} · argv: {JSON.stringify(verification.argv)}</span>{verification.error && <span className="block text-state-failed">Failure: {verification.error}</span>}<pre className="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-words bg-muted/20 p-1">{verification.output || 'No verification output'}{verification.output_truncated ? '\n[output truncated by server]' : ''}</pre></span></div></div>
}
