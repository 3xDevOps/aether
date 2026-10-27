import { useEffect, useState } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { type Api } from '@/lib/api'
import { useStore } from '@/store'
import type { RunLimits, ServerRunLimits } from '@/lib/types'

const gib = 2 ** 30

/** What the three inputs hold; memory is in GiB. Empty is unset. */
export type LimitsDraft = { cpus: string; memory: string; pids: string }

export const emptyLimits: LimitsDraft = { cpus: '', memory: '', pids: '' }

export function draftOf(limits: RunLimits): LimitsDraft {
  return {
    cpus: limits.cpus ? String(limits.cpus) : '',
    memory: limits.memory_bytes ? String(limits.memory_bytes / gib) : '',
    pids: limits.pids ? String(limits.pids) : '',
  }
}

function positive(text: string, whole: boolean): number | undefined {
  if (text.trim() === '') return 0
  const value = Number(text)
  return Number.isFinite(value) && value > 0 && (!whole || Number.isInteger(value)) ? value : undefined
}

/** Unset fields are omitted; `error` names the first input that is not a positive number. */
export function parseLimits(draft: LimitsDraft): { limits: RunLimits; error?: undefined } | { limits?: undefined; error: string } {
  const cpus = positive(draft.cpus, false)
  const memory = positive(draft.memory, false)
  const pids = positive(draft.pids, true)
  if (cpus === undefined) return { error: 'CPUs must be a number above 0.' }
  if (memory === undefined) return { error: 'Memory must be a number of GiB above 0.' }
  if (pids === undefined) return { error: 'Processes must be a whole number above 0.' }
  return { limits: { ...(cpus && { cpus }), ...(memory && { memory_bytes: Math.round(memory * gib) }), ...(pids && { pids }) } }
}

export function limitsText(limits: RunLimits): string {
  return [
    limits.cpus !== undefined && `${limits.cpus} CPUs`,
    limits.memory_bytes !== undefined && `${limits.memory_bytes / gib} GiB memory`,
    limits.pids !== undefined && `${limits.pids} processes`,
  ].filter(Boolean).join(', ')
}

/** The server's run limits, or the error that stopped the read. */
export function useServerRunLimits(client: Api, enabled: boolean): { limits: ServerRunLimits | null; error: string | null; set: (next: ServerRunLimits) => void; reload: () => void } {
  const [limits, set] = useState<ServerRunLimits | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [seq, setSeq] = useState(0)
  const identityKey = useStore((s) => s.identityKey)
  const connectionEpoch = useStore((s) => s.connectionEpoch)
  useEffect(() => {
    set(null)
    if (!enabled) return
    let live = true
    client.serverRunLimits().then(
      (read) => {
        if (!live) return
        set(read)
        setError(null)
      },
      (err: unknown) => live && setError(err instanceof Error ? err.message : String(err)),
    )
    return () => {
      live = false
    }
  }, [client, enabled, seq, identityKey, connectionEpoch])
  return { limits, error, set, reload: () => setSeq((n) => n + 1) }
}

export function RunLimitsFields({ idPrefix, value, onChange, placeholders, disabled }: {
  idPrefix: string
  value: LimitsDraft
  onChange: (next: LimitsDraft) => void
  /** Shown while a field is empty: what the run gets without it. */
  placeholders: RunLimits | undefined
  disabled?: boolean
}) {
  const shown = placeholders ? draftOf(placeholders) : emptyLimits
  const fields = [
    ['cpus', 'CPUs', shown.cpus],
    ['memory', 'Memory (GiB)', shown.memory],
    ['pids', 'Processes', shown.pids],
  ] as const
  return (
    <div className="grid grid-cols-3 gap-2 max-sm:grid-cols-1">
      {fields.map(([key, label, placeholder]) => (
        <div key={key} className="flex min-w-0 flex-col gap-1">
          <Label htmlFor={`${idPrefix}-${key}`}>{label}</Label>
          <Input
            id={`${idPrefix}-${key}`}
            type="number"
            inputMode="decimal"
            min={key === 'pids' ? 1 : undefined}
            step={key === 'pids' ? 1 : 'any'}
            placeholder={placeholder || 'Automatic'}
            value={value[key]}
            disabled={disabled}
            onChange={(event) => onChange({ ...value, [key]: event.target.value })}
          />
        </div>
      ))}
    </div>
  )
}
