import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import type { EvidencePacket, EvidencePatchResult, EvidenceTranscriptResult } from '@/lib/types'
import { CandidateReview } from '@/routes/terminal/candidate-review'
import { useStore } from '@/store'
const emptyPackets: EvidencePacket[] = []

function when(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}

function sourceSummary(packet: EvidencePacket): string {
  const sources = packet.sources ?? []
  if (!sources.length) return 'No source availability recorded'
  const available = sources.filter((source) => source.available).length
  const truncated = sources.filter((source) => source.truncated).length
  return `${available}/${sources.length} sources available${truncated ? `, ${truncated} truncated` : ''}`
}

export interface EvidenceDrawerProps {
  runID: string
  workspaceID: string
  client?: Api
  onAnswer?: (fact: string) => void
}

export function EvidenceDrawer({ runID, workspaceID, client = api, onAnswer }: EvidenceDrawerProps) {
  const packets = useStore((state) => state.evidencePackets[runID] ?? emptyPackets)
  const nextBefore = useStore((state) => state.evidenceNextBefore[runID])
  const pagination = useStore((state) => state.evidencePagination[runID])
  const loading = useStore((state) => state.evidenceLoading[runID] === true)
  const error = useStore((state) => state.evidenceError[runID])
  const initializePagination = useStore((state) => state.initializeEvidencePagination)
  const setLoading = useStore((state) => state.setEvidenceLoading)
  const setError = useStore((state) => state.setEvidenceError)
  const setPage = useStore((state) => state.setEvidencePage)
  const select = useStore((state) => state.selectEvidence)
  const selectedID = useStore((state) => state.selectedEvidence[runID])
  const [open, setOpen] = useState(false)
  const [packet, setPacket] = useState<EvidencePacket | null>(null)
  const [patch, setPatch] = useState<EvidencePatchResult | null>(null)
  const [transcript, setTranscript] = useState<EvidenceTranscriptResult | null>(null)
  const [view, setView] = useState<'summary' | 'patch' | 'transcript'>('summary')
  const canLoadOlder = pagination?.initialized === true && !pagination.exhausted && nextBefore !== undefined

  const loadList = async (before?: string) => {
    // Define the list before the first await. An evidence event landing during
    // this request sees a live list and triggers its own reconciliation fetch.
    if (before === undefined) initializePagination(runID)
    setLoading(runID, true)
    setError(runID)
    try {
      const result = await client.runEvidenceList({
        workspace_id: workspaceID,
        run_id: runID,
        before,
        limit: 50,
      })
      setPage(runID, result.packets, result.next_before, before !== undefined)
    } catch (cause) {
      setError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      setLoading(runID, false)
    }
  }

  useEffect(() => {
    if (open && !packets.length && !loading && !error) void loadList()
    // The drawer deliberately owns its first read. Events update an already
    // populated list in the sync layer and do not make closed drawers noisy.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, runID, workspaceID])

  useEffect(() => {
    if (!selectedID) {
      setPacket(null)
      return
    }
    const cached = packets.find((item) => item.id === selectedID)
    if (cached) setPacket(cached)
  }, [packets, selectedID])

  useEffect(() => {
    if (!selectedID) {
      setPacket(null)
      setPatch(null)
      setTranscript(null)
      setView('summary')
      return
    }
    setPatch(null)
    setTranscript(null)
    setView('summary')
    let cancelled = false
    void client
      .runEvidenceGet({ workspace_id: workspaceID, packet_id: selectedID })
      .then((result) => {
        if (!cancelled) setPacket(result.packet)
      })
      .catch((cause) => {
        if (!cancelled) setError(runID, cause instanceof Error ? cause.message : String(cause))
      })
    return () => {
      cancelled = true
    }
  }, [client, runID, selectedID, setError, workspaceID])

  const openPacket = (id: string) => {
    select(runID, id)
    setOpen(true)
  }

  const answerFact = (fact: string) => {
    setOpen(false)
    onAnswer?.(fact)
  }

  const loadPatch = async () => {
    if (!packet) return
    const requestedPacketID = packet.id
    setView('patch')
    if (patch?.packet.id === requestedPacketID) return
    try {
      const result = await client.runEvidencePatch({
        workspace_id: workspaceID,
        packet_id: requestedPacketID,
        max_bytes: 512 * 1024,
      })
      if (
        result.packet.id !== requestedPacketID ||
        useStore.getState().selectedEvidence[runID] !== requestedPacketID
      ) return
      setPatch(result)
    } catch (cause) {
      if (useStore.getState().selectedEvidence[runID] === requestedPacketID) {
        setError(runID, cause instanceof Error ? cause.message : String(cause))
      }
    }
  }

  const loadTranscript = async () => {
    if (!packet) return
    const requestedPacketID = packet.id
    setView('transcript')
    if (transcript?.packet.id === requestedPacketID) return
    try {
      const result = await client.runEvidenceTranscript({
        workspace_id: workspaceID,
        packet_id: requestedPacketID,
        max_bytes: 512 * 1024,
      })
      if (
        result.packet.id !== requestedPacketID ||
        useStore.getState().selectedEvidence[runID] !== requestedPacketID
      ) return
      setTranscript(result)
    } catch (cause) {
      if (useStore.getState().selectedEvidence[runID] === requestedPacketID) {
        setError(runID, cause instanceof Error ? cause.message : String(cause))
      }
    }
  }

  return (
    <section className="relative min-w-0" aria-label="Run evidence">
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        Evidence{packets.length ? ` (${packets.length})` : ''}
      </Button>
      {open && (
        <div className="fixed inset-x-0 top-[calc(var(--title-bar-height)+var(--safe-top))] bottom-0 z-[80] flex min-h-0 w-full flex-col overflow-hidden border border-border bg-background shadow-lg md:absolute md:inset-x-0 md:top-8 md:right-0 md:bottom-auto md:left-auto md:z-40 md:max-h-[min(38rem,calc(100dvh-5rem))] md:w-[min(38rem,calc(100vw-2rem))]">
          <header className="flex items-center justify-between gap-2 border-b border-border px-3 py-2">
            <div className="min-w-0">
              <h2 className="truncate text-[13px] font-semibold">Retained evidence</h2>
              <p className="text-[11px] text-muted-foreground">Recorded observations, not verification</p>
            </div>
            <Button type="button" size="icon" variant="ghost" aria-label="Close evidence" onClick={() => setOpen(false)}>×</Button>
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto">
            <div className="px-3">
              <CandidateReview workspaceID={workspaceID} currentRunID={runID} client={client} />
            </div>
            {error && <div role="alert" className="flex items-start justify-between gap-2 border-b border-state-failed/30 bg-state-failed/10 px-3 py-2 text-[12px] text-state-failed"><span>{error}</span>{!selectedID && <Button type="button" size="sm" variant="ghost" disabled={loading} onClick={() => void loadList()}>Retry evidence</Button>}</div>}
            {!selectedID ? (
              <div className="p-3">
                {loading && <p className="text-[12px] text-muted-foreground">Loading evidence…</p>}
                {!loading && !packets.length && <p className="text-[12px] text-muted-foreground">No retained packets for this run.</p>}
                <div className="divide-y divide-border">
                  {packets.map((item) => (
                    <button
                      key={item.id}
                      type="button"
                      className="block w-full py-2 text-left hover:bg-toolbar-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      onClick={() => openPacket(item.id)}
                    >
                      <div className="flex items-baseline justify-between gap-2">
                        <span className="text-[12px] font-medium">{item.trigger} capture</span>
                        <time className="text-[11px] text-muted-foreground">{when(item.captured_at)}</time>
                      </div>
                      <p className="truncate text-[12px] text-muted-foreground">{item.objective || 'Objective not recorded'}</p>
                      <p className="text-[11px] text-muted-foreground">{item.changed_files?.length ?? 0} changed files · {sourceSummary(item)}</p>
                    </button>
                  ))}
                </div>
                {canLoadOlder && <Button type="button" variant="ghost" size="sm" className="mt-2" disabled={loading} onClick={() => void loadList(nextBefore)}>Load older</Button>}
              </div>
            ) : (
              <div className="p-3">
                <Button type="button" variant="ghost" size="sm" onClick={() => select(runID)}>Back to packets</Button>
                {packet ? (
                  <>
                    <div className="mt-2 border-b border-border pb-2">
                      <div className="flex items-baseline justify-between gap-2">
                        <h3 className="text-[13px] font-semibold">{packet.trigger} capture</h3>
                        <time className="text-[11px] text-muted-foreground">{when(packet.captured_at)}</time>
                      </div>
                      <p className="mt-1 text-[12px]">{packet.objective || 'Objective not recorded'}</p>
                      <dl className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                        <div><dt className="inline font-medium">Base: </dt><dd className="inline break-all">{packet.base_revision || 'Unavailable'}</dd></div>
                        <div><dt className="inline font-medium">Retained: </dt><dd className="inline break-all">{packet.retained_revision || 'Unavailable'}</dd></div>
                        <div><dt className="inline font-medium">Boundary: </dt><dd className="inline">{packet.event_boundary}</dd></div>
                        <div><dt className="inline font-medium">Sources: </dt><dd className="inline">{sourceSummary(packet)}</dd></div>
                      </dl>
                    </div>
                    <div className="mt-2 flex flex-wrap gap-1" role="tablist" aria-label="Evidence views">
                      <Button type="button" size="sm" variant={view === 'summary' ? 'secondary' : 'ghost'} role="tab" aria-selected={view === 'summary'} onClick={() => setView('summary')}>Summary</Button>
                      <Button type="button" size="sm" variant={view === 'patch' ? 'secondary' : 'ghost'} role="tab" aria-selected={view === 'patch'} onClick={() => void loadPatch()}>Patch</Button>
                      <Button type="button" size="sm" variant={view === 'transcript' ? 'secondary' : 'ghost'} role="tab" aria-selected={view === 'transcript'} onClick={() => void loadTranscript()}>Transcript</Button>
                    </div>
                    {view === 'summary' && <EvidenceSummary packet={packet} onAnswer={onAnswer ? answerFact : undefined} />}
                    {view === 'patch' && <EvidenceText value={patch?.packet.id === packet.id ? patch.patch : undefined} truncated={patch?.packet.id === packet.id ? patch.truncated : undefined} empty="Patch unavailable" />}
                    {view === 'transcript' && <EvidenceText value={transcript?.packet.id === packet.id ? decodeTranscript(transcript.data_base64) : undefined} truncated={transcript?.packet.id === packet.id ? transcript.truncated : undefined} empty="Transcript unavailable" />}
                  </>
                ) : <p className="mt-2 text-[12px] text-muted-foreground">Loading packet…</p>}
              </div>
            )}
          </div>
        </div>
      )}
    </section>
  )
}

function EvidenceSummary({ packet, onAnswer }: { packet: EvidencePacket; onAnswer?: (fact: string) => void }) {
  return (
    <div className="mt-3 space-y-3 text-[12px]">
      <div>
        <h4 className="font-medium">Changed files</h4>
        {packet.changed_files?.length ? <ul className="mt-1 space-y-1 text-muted-foreground">{packet.changed_files.map((file) => <li key={file.path} className="flex justify-between gap-2"><code className="truncate">{file.path}</code><span className="shrink-0">{file.status || 'changed'} {file.additions !== undefined || file.deletions !== undefined ? `+${file.additions ?? 0}/-${file.deletions ?? 0}` : ''}</span></li>)}</ul> : <p className="mt-1 text-muted-foreground">No changed files retained.</p>}
      </div>
      <div>
        <h4 className="font-medium">Source availability</h4>
        <ul className="mt-1 space-y-1 text-muted-foreground">{(packet.sources ?? []).map((source) => <li key={source.name}>{source.name}: {source.available ? 'available' : `unavailable${source.reason ? ` (${source.reason})` : ''}`}{source.truncated ? ', truncated' : ''}</li>)}</ul>
      </div>
      {packet.unresolved_facts?.length ? <div><h4 className="font-medium">Unresolved questions</h4><ul className="mt-1 space-y-1">{packet.unresolved_facts.map((fact) => <li key={fact} className="flex items-start justify-between gap-2"><span>{fact}</span>{onAnswer && <Button type="button" size="sm" variant="outline" onClick={() => onAnswer(fact)}>Answer</Button>}</li>)}</ul></div> : <p className="text-muted-foreground">No unresolved questions recorded.</p>}
      {packet.next_action && <p><span className="font-medium">Next action: </span>{packet.next_action}</p>}
      <p className="text-muted-foreground"><span className="font-medium">Provenance: </span>{packet.provenance || 'Not recorded'}</p>
    </div>
  )
}

function EvidenceText({ value, truncated, empty }: { value?: string; truncated?: boolean; empty: string }) {
  return <div className="mt-3"><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words border border-border bg-muted/20 p-2 font-mono text-[11px]">{value || empty}</pre>{truncated && <p className="mt-1 text-[11px] text-muted-foreground">This source was truncated by the server.</p>}</div>
}

function decodeTranscript(value: string): string {
  try {
    const bytes = Uint8Array.from(atob(value), (char) => char.charCodeAt(0))
    return new TextDecoder().decode(bytes)
  } catch {
    return 'Transcript data unavailable'
  }
}
