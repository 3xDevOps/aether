import { useRef, useState } from 'react'
import { ChevronRight, Undo2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { AssistantAction, PreferenceFacts } from '@/lib/assistant-actions'
import { errorSentence } from '@/lib/format'
import { type Restore, restorePreference } from '@/lib/preferences'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { assistantSessionID } from '@/store/assistant'

type Facts = Exclude<PreferenceFacts, { kind: 'used' }>

const undoneKept = 200

/** Kept per member, so a shared browser holds no other member's preference ids. */
const undoneKey = (member: string | undefined) => `aether.preferences.undone.${member ?? ''}`

function undoneIDs(member: string | undefined): string[] {
  try {
    localStorage.removeItem('aether.preferences.undone')
    const stored: unknown = JSON.parse(localStorage.getItem(undoneKey(member)) ?? '[]')
    return Array.isArray(stored) ? stored.filter((id): id is string => typeof id === 'string') : []
  } catch {
    return []
  }
}

function markUndone(member: string | undefined, id: string) {
  try {
    localStorage.setItem(undoneKey(member), JSON.stringify([...undoneIDs(member).filter((kept) => kept !== id), id].slice(-undoneKept)))
  } catch {
    // Without storage the notice still says Undone until it remounts.
  }
}

async function undo(facts: Facts, progress: Restore): Promise<void> {
  const member = useStore.getState().info?.member?.id
  if (facts.kind === 'saved') await api.preferenceDelete(facts.id, { expected_member_id: member, expected_updated_at: facts.updated_at })
  else if (facts.kind === 'updated') {
    const { text, workspace_id } = facts.previous
    await api.preferenceUpdate({ id: facts.id, text, ...(workspace_id ? { workspace_id } : { clear_workspace: true }), expected_member_id: member, expected_updated_at: facts.updated_at })
  } else await restorePreference(api, facts, progress)
}

/** Undo for a saved, updated or forgotten preference, with the server's refusal shown as it came. */
function UndoPreference({ facts }: { facts: Facts }) {
  const member = useStore((s) => s.info?.member?.id)
  const undoneID = `${facts.kind}:${facts.id}:${facts.updated_at}`
  const [state, setState] = useState<{ kind: 'idle' } | { kind: 'busy' } | { kind: 'undone' } | { kind: 'failed'; error: string }>(() => undoneIDs(member).includes(undoneID) ? { kind: 'undone' } : { kind: 'idle' })
  const progress = useRef<Restore>({})
  if (state.kind === 'undone') return <span role="status" className="text-ui-sm text-muted">Undone</span>
  return (
    <>
      <Button
        variant="secondary"
        size="sm"
        disabled={state.kind === 'busy'}
        onClick={() => {
          setState({ kind: 'busy' })
          undo(facts, progress.current).then(() => {
            markUndone(member, undoneID)
            useStore.getState().notePreferenceUndo()
            setState({ kind: 'undone' })
          }, (err: unknown) => setState({ kind: 'failed', error: errorSentence(err) }))
        }}
      >
        <Undo2 aria-hidden />
        Undo
      </Button>
      {state.kind === 'failed' && <span role="alert" className="min-w-0 text-ui-sm break-words text-state-failed">{state.error}</span>}
    </>
  )
}

export function PreferenceDetail({ facts }: { facts: PreferenceFacts }) {
  if (facts.kind === 'updated') return <p className="pl-5 text-ui-sm break-words text-muted">was: {facts.previous.text}</p>
  return null
}

export function PreferenceUndo({ facts }: { facts: PreferenceFacts }) {
  return facts.kind === 'used' ? null : <UndoPreference facts={facts} />
}

/** "Read N of your preferences": the list is the reason the answer looks the way it does. */
export function UsedPreferences({ action, items }: { action: AssistantAction; items: { id: string; text: string; scope: string }[] }) {
  const key = `${action.id}:used`
  const open = useStore((s) => s.expandedRows[assistantSessionID]?.[key] ?? false)
  const setRow = useStore((s) => s.setSessionRow)
  if (items.length === 0) return null
  return (
    <div className="flex min-w-0 flex-col gap-1 pl-5">
      <Button variant="quiet" size="sm" aria-expanded={open} className="self-start" onClick={() => setRow(assistantSessionID, key, !open)}>
        <ChevronRight aria-hidden className={cn('size-3.5 transition-transform', open && 'rotate-90')} />
        {open ? 'Hide preferences' : 'Show preferences'}
      </Button>
      {open && (
        <ul aria-label="Preferences used" className="flex min-w-0 flex-col gap-1 text-ui-sm">
          {items.map((item, i) => (
            <li key={item.id || i} className="min-w-0 break-words">
              {item.text}
              <span className="text-muted"> · {item.scope === 'workspace' ? 'This workspace' : 'All my workspaces'}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
