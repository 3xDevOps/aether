import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import type { DevArtifact, EvidencePacket, EvidencePatchResult, EvidenceTranscriptResult } from '@/lib/types'
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
    setPacket(null)
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
    <section className="min-w-0" aria-label="Run evidence">
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
        <div className="fixed inset-x-0 top-[calc(var(--title-bar-height)+var(--safe-top))] bottom-0 z-[80] flex min-h-0 w-full flex-col overflow-hidden border border-border bg-background shadow-lg md:top-[calc(var(--title-bar-height)+var(--safe-top)+0.75rem)] md:right-3 md:bottom-auto md:left-auto md:z-40 md:max-h-[min(38rem,calc(100dvh-var(--title-bar-height)-var(--safe-top)-var(--status-bar-height)-1.5rem))] md:w-[min(38rem,calc(100vw-2rem))]">
          <header className="flex shrink-0 items-center justify-between gap-2 border-b border-border px-3 py-2">
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
            <div className="border-b border-border px-3 py-2 text-[11px] text-muted-foreground">
              Retain only reviewed captures. Images, URLs and notes may contain credentials or customer data; Aether does not reliably redact them.
              Retained copies use evidence access and expiry, not private live-session permissions. Nothing is automatically retained or attached to a public PR.
            </div>
            <CaptureRetention
              key={`${workspaceID}:${runID}`}
              runID={runID}
              client={client}
              onRetained={(id) => { void loadList(); openPacket(id) }}
            />
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
                        <div><dt className="inline font-medium">Packet-retain revision: </dt><dd className="inline break-all">{packet.retained_revision || 'Unavailable'}</dd></div>
                        <div><dt className="inline font-medium">Boundary: </dt><dd className="inline">{packet.event_boundary}</dd></div>
                        <div><dt className="inline font-medium">Sources: </dt><dd className="inline">{sourceSummary(packet)}</dd></div>
                      </dl>
                      <p className="mt-1 text-[11px] text-muted-foreground">The packet-retain revision is a later snapshot, not the Git boundary of an earlier capture.</p>
                      {packet.expires_at && <p className="mt-1 text-[11px] text-muted-foreground">Evidence expires {when(packet.expires_at)}.</p>}
                      {packet.availability === 'expired' && <p role="status" className="mt-1 text-[12px] text-state-failed">This packet has expired. Retained bytes are no longer available.</p>}
                    </div>
                    <div className="mt-2 flex flex-wrap gap-1" role="tablist" aria-label="Evidence views">
                      <Button type="button" size="sm" variant={view === 'summary' ? 'secondary' : 'ghost'} role="tab" aria-selected={view === 'summary'} onClick={() => setView('summary')}>Summary</Button>
                      <Button type="button" size="sm" variant={view === 'patch' ? 'secondary' : 'ghost'} role="tab" aria-selected={view === 'patch'} onClick={() => void loadPatch()}>Patch</Button>
                      <Button type="button" size="sm" variant={view === 'transcript' ? 'secondary' : 'ghost'} role="tab" aria-selected={view === 'transcript'} onClick={() => void loadTranscript()}>Transcript</Button>
                    </div>
                    {view === 'summary' && (
                      <>
                        <EvidenceSummary packet={packet} onAnswer={onAnswer ? answerFact : undefined} />
                        <RetainedCaptures key={packet.id} packet={packet} client={client} />
                      </>
                    )}
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

interface CaptureRetentionProps {
  runID: string
  client: Api
  onRetained: (packetID: string) => void
}

function CaptureRetention({ runID, client, onRetained }: CaptureRetentionProps) {
  const [captures, setCaptures] = useState<DevArtifact[]>([])
  const [selected, setSelected] = useState<string[]>([])
  const [notes, setNotes] = useState('')
  const [next, setNext] = useState<string>()
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(false)
  const [retaining, setRetaining] = useState(false)
  const [listError, setListError] = useState<string>()
  const [retainError, setRetainError] = useState<string>()
  const [retainedID, setRetainedID] = useState<string>()
  const [idempotencyKey, setIdempotencyKey] = useState<string>()
  const alive = useRef(true)
  const listRequest = useRef(0)
  const pending = useRef(false)
  const attempt = useRef<{ fingerprint: string; key: string } | null>(null)
  const noteBytes = new TextEncoder().encode(notes).byteLength

  const loadCaptures = async (after?: string) => {
    const request = ++listRequest.current
    setLoading(true)
    setListError(undefined)
    try {
      const result = await client.devArtifactList({ run_id: runID, after, limit: 64 })
      if (!alive.current || request !== listRequest.current) return
      setCaptures((current) => after
        ? [...new Map([...current, ...result.artifacts].map((capture) => [capture.id, capture])).values()]
        : result.artifacts)
      if (!after) setSelected((current) => current.filter((id) => result.artifacts.some((capture) => capture.id === id)))
      setNext(result.next)
      setTruncated(result.truncated)
    } catch (cause) {
      if (alive.current && request === listRequest.current) setListError(errorMessage(cause))
    } finally {
      if (alive.current && request === listRequest.current) setLoading(false)
    }
  }

  useEffect(() => {
    alive.current = true
    void loadCaptures()
    return () => {
      alive.current = false
      listRequest.current++
    }
    // The parent keys this component by workspace/run; reopening reads anew.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, runID])

  const retain = async () => {
    if (pending.current || !selected.length || selected.length > 64 || noteBytes > 4096) return
    const artifactIDs = [...selected].sort()
    const fingerprint = JSON.stringify([artifactIDs, notes])
    if (attempt.current?.fingerprint !== fingerprint) {
      attempt.current = { fingerprint, key: crypto.randomUUID() }
    }
    const key = attempt.current.key
    pending.current = true
    setRetaining(true)
    setIdempotencyKey(key)
    setRetainError(undefined)
    setRetainedID(undefined)
    try {
      const result = await client.devArtifactRetain({
        run_id: runID,
        artifact_ids: artifactIDs,
        verification_notes: notes || undefined,
        idempotency_key: key,
      })
      if (!alive.current) return
      setRetainedID(result.packet_id)
      setSelected([])
      setNotes('')
      attempt.current = null
      onRetained(result.packet_id)
    } catch (cause) {
      if (alive.current) setRetainError(errorMessage(cause))
    } finally {
      pending.current = false
      if (alive.current) setRetaining(false)
    }
  }

  return (
    <details className="border-b border-border px-3 py-2">
      <summary className="cursor-pointer text-[12px] font-medium">Select transient captures to retain</summary>
      <p className="mt-2 text-[11px] text-muted-foreground">Verify first, then deliberately retain before report or cleanup. A transient capture ID alone is not durable evidence.</p>
      <div className="mt-2 flex items-center justify-between gap-2">
        <span className="text-[11px] text-muted-foreground">{selected.length}/64 selected</span>
        <Button type="button" size="sm" variant="ghost" disabled={loading || retaining} onClick={() => void loadCaptures()}>Refresh captures</Button>
      </div>
      {loading && <p role="status" className="text-[12px] text-muted-foreground">Loading transient captures…</p>}
      {listError && <p role="alert" className="my-2 break-words text-[12px] text-state-failed">{listError}</p>}
      {!loading && !listError && !captures.length && <p className="text-[12px] text-muted-foreground">No transient captures available.</p>}
      <div className="max-h-80 space-y-2 overflow-y-auto">
        {captures.map((capture) => (
          <div key={capture.id} className="rounded border border-border p-2">
            <label className="flex items-center gap-2 text-[12px]">
              <input
                type="checkbox"
                checked={selected.includes(capture.id)}
                disabled={retaining || (!selected.includes(capture.id) && selected.length >= 64)}
                onChange={(event) => setSelected((current) => event.target.checked ? [...current, capture.id] : current.filter((id) => id !== capture.id))}
              />
              Select {capture.source} capture <span className="break-all font-mono text-[10px]">{capture.id}</span>
            </label>
            <CaptureMetadata capture={capture} />
            <CaptureImage capture={capture} client={client} />
          </div>
        ))}
      </div>
      {truncated && <p className="mt-2 text-[11px] text-muted-foreground">The capture list is truncated; only the listed captures can be selected here.</p>}
      {next && <Button type="button" size="sm" variant="ghost" disabled={loading || retaining} onClick={() => void loadCaptures(next)}>Load more captures</Button>}
      <label className="mt-3 block text-[12px]">
        Verification notes
        <textarea
          className="mt-1 min-h-20 w-full rounded border border-input bg-background px-2 py-1 text-[12px]"
          value={notes}
          disabled={retaining}
          maxLength={4096}
          onChange={(event) => setNotes(event.target.value)}
          placeholder="What you actually checked, the result, and any limits or missing observations."
        />
      </label>
      <p className={`text-[11px] ${noteBytes > 4096 ? 'text-state-failed' : 'text-muted-foreground'}`}>{noteBytes}/4096 UTF-8 bytes. Notes are observations, not an automatic verification claim.</p>
      <Button type="button" size="sm" className="mt-2" disabled={retaining || !selected.length || noteBytes > 4096} onClick={() => void retain()}>{retaining ? 'Retaining…' : 'Retain selected captures'}</Button>
      {idempotencyKey && <p className="mt-2 break-all text-[10px] text-muted-foreground">Idempotency key: {idempotencyKey}</p>}
      {retainError && <div role="alert" className="mt-2 text-[12px] text-state-failed"><p className="break-words">{retainError}</p><p>No automatic retry was made. Retrying the unchanged selection and notes reuses this key; inspect retained evidence if the outcome is uncertain.</p></div>}
      {retainedID && <p role="status" className="mt-2 break-words text-[12px]">Retained packet <code>{retainedID}</code>. Use this packet ID with the existing report <code>--evidence-ref</code>; retaining does not report an outcome.</p>}
    </details>
  )
}

function RetainedCaptures({ packet, client }: { packet: EvidencePacket; client: Api }) {
  const expired = packet.availability === 'expired' || Boolean(packet.expires_at && Date.parse(packet.expires_at) <= Date.now())
  return (
    <div className="mt-3 space-y-2 text-[12px]">
      {packet.verification_notes && <div><h4 className="font-medium">Verification notes</h4><p className="mt-1 whitespace-pre-wrap break-words text-muted-foreground">{packet.verification_notes}</p></div>}
      <h4 className="font-medium">Retained captures</h4>
      {!packet.captures?.length && <p className="text-muted-foreground">{expired ? 'Capture bytes are expired; no retained capture metadata is available.' : 'No captures were retained in this packet.'}</p>}
      {packet.captures?.map((capture) => (
        <div key={capture.id} className="rounded border border-border p-2">
          <CaptureMetadata capture={capture} />
          <CaptureImage capture={capture} client={client} packetID={packet.id} unavailable={expired ? 'Expired: retained image bytes are no longer available.' : undefined} />
        </div>
      ))}
    </div>
  )
}

function CaptureMetadata({ capture }: { capture: DevArtifact }) {
  return (
    <dl className="mt-2 space-y-1 break-words text-[11px] text-muted-foreground">
      <div><dt className="inline font-medium">Original source: </dt><dd className="inline">{capture.source} · {capture.id}</dd></div>
      <div><dt className="inline font-medium">Captured: </dt><dd className="inline">{when(capture.captured_at)}</dd></div>
      <div><dt className="inline font-medium">Session / incarnation: </dt><dd className="inline break-all">{capture.incarnation || 'Not recorded'}</dd></div>
      {capture.terminal_id && <div><dt className="inline font-medium">Terminal: </dt><dd className="inline">{capture.terminal_id} · screen revision {capture.screen_revision ?? 'unknown'}</dd></div>}
      {capture.page_id && <div><dt className="inline font-medium">Page: </dt><dd className="inline">{capture.page_id} · revision {capture.page_revision ?? 'unknown'}</dd></div>}
      <div><dt className="inline font-medium">Original geometry: </dt><dd className="inline">{capture.width} × {capture.height} pixels{capture.cols !== undefined && capture.rows !== undefined ? ` · ${capture.cols} × ${capture.rows} cells` : ''} · geometry revision {capture.geometry_revision ?? 'unknown'}{capture.viewport_id ? ` · viewport ${capture.viewport_id}` : ''}</dd></div>
      {capture.url && <div><dt className="inline font-medium">Original URL: </dt><dd className="inline break-all">{capture.url}</dd></div>}
      <div><dt className="inline font-medium">Capture Git HEAD: </dt><dd className="inline break-all">{capture.git_head || 'Unknown — not observed at capture time'}</dd></div>
      <div><dt className="inline font-medium">Capture checkout: </dt><dd className="inline">{capture.dirty === undefined ? 'Unknown — cleanliness was not observed' : capture.dirty ? 'Observed dirty' : 'Observed clean'}</dd></div>
      <div><dt className="inline font-medium">Bytes: </dt><dd className="inline">{capture.bytes} · {capture.content_type}</dd></div>
      {capture.truncated && <div className="text-state-failed"><dt className="inline font-medium">Truncated: </dt><dd className="inline">This capture omits some source content.</dd></div>}
    </dl>
  )
}

interface CaptureImageProps {
  capture: DevArtifact
  client: Api
  packetID?: string
  unavailable?: string
}

function CaptureImage({ capture, client, packetID, unavailable }: CaptureImageProps) {
  const [url, setURL] = useState<string>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()
  const request = useRef<AbortController | null>(null)
  const objectURL = useRef<string | undefined>(undefined)

  useEffect(() => {
    setURL(undefined)
    setLoading(false)
    setError(undefined)
    return () => {
      request.current?.abort()
      if (objectURL.current) URL.revokeObjectURL(objectURL.current)
      objectURL.current = undefined
    }
  }, [client, capture.id, capture.run_id, packetID, unavailable])

  const loadImage = async () => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setLoading(true)
    setError(undefined)
    if (objectURL.current) URL.revokeObjectURL(objectURL.current)
    objectURL.current = undefined
    setURL(undefined)
    try {
      const blob = await client.devArtifactDownload({
        run_id: capture.run_id,
        artifact_id: capture.id,
        evidence_packet_id: packetID,
      }, controller.signal)
      if (controller.signal.aborted) return
      if (blob.size !== capture.bytes) throw new Error('Capture download was interrupted or the retained byte count does not match.')
      objectURL.current = URL.createObjectURL(blob)
      setURL(objectURL.current)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }

  return (
    <div className="mt-2 space-y-2">
      {unavailable ? <p role="status" className="text-[12px] text-state-failed">{unavailable}</p> : (
        <Button type="button" size="sm" variant="outline" disabled={loading} onClick={() => void loadImage()}>{loading ? 'Loading image…' : url ? 'Reload image' : 'Load image / download'}</Button>
      )}
      {error && <p role="alert" className="break-words text-[12px] text-state-failed">Image unavailable (it may be missing, expired, or access may have changed): {error}</p>}
      {url && !unavailable && (
        <>
          {capture.content_type === 'image/png' && (
            // The authenticated helper fetches private bytes; an object URL
            // cannot be routed through the Next image optimizer.
            // eslint-disable-next-line @next/next/no-img-element
            <img src={url} alt={`${packetID ? 'Retained' : 'Transient'} ${capture.source} capture from ${when(capture.captured_at)}`} className="h-auto max-w-full rounded border border-border" onError={() => setError('The downloaded capture could not be decoded as an image.')} />
          )}
          <a href={url} download={`${capture.id}.png`} className="inline-block text-[12px] underline">Download original PNG</a>
        </>
      )}
    </div>
  )
}

function errorMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause)
}
