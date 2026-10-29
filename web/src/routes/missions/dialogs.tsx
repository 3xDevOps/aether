import { useEffect, useRef, useState } from 'react'
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
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import { ModeControl } from '@/components/launch/mode-control'
import { agentLabel, integratorModes } from '@/components/launch/modes'
import { memberName } from '@/lib/member-name'
import type { AgentInfo, LaunchMode, Mission, MissionExecutionChoice } from '@/lib/types'
import { useStore } from '@/store'

function agentKey(choice: MissionExecutionChoice): string {
  return JSON.stringify([choice.harness, choice.model ?? '', choice.effort ?? ''])
}

function newKey(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `mission-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

/** mission.replace-integrator takes any execution choice's account and agent, in Standard or Enhanced. */
export function ReplaceIntegrator({ mission, agents, client, onClose, onReplaced }: {
  mission: Mission
  agents: AgentInfo[] | null
  client: Api
  onClose: () => void
  onReplaced: () => void
}) {
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const navigate = useStore((s) => s.navigate)
  const ownID = useStore((s) => s.info?.member?.id)
  const choices = mission.execution_choices.filter(
    (choice, index, all) =>
      all.findIndex((other) => other.account_member_id === choice.account_member_id && agentKey(other) === agentKey(choice)) === index,
  )
  const accountIDs = [...new Set(choices.map((choice) => choice.account_member_id))]
  const current =
    choices.find(
      (choice) => choice.account_member_id === mission.integrator.account_member_id && agentKey(choice) === agentKey(mission.integrator),
    ) ?? choices[0]
  const [account, setAccount] = useState(current?.account_member_id ?? '')
  const [agent, setAgent] = useState(current ? agentKey(current) : '')
  const options = choices.filter((choice) => choice.account_member_id === account)
  const chosen = options.find((choice) => agentKey(choice) === agent)
  const [preferredMode, setPreferredMode] = useState<LaunchMode>('acp')
  const [error, setError] = useState<string | null>(null)
  const [accountAgents, setAccountAgents] = useState<{ account: string; agents?: AgentInfo[]; error?: string } | null>(null)
  const [listAttempt, setListAttempt] = useState(0)
  useEffect(() => {
    let live = true
    setAccountAgents(null)
    client.agentList(account === ownID ? undefined : account).then(
      (list) => { if (live) setAccountAgents({ account, agents: list }) },
      (err: unknown) => { if (live) setAccountAgents({ account, error: message(err) }) },
    )
    return () => { live = false }
  }, [account, ownID, client, listAttempt])
  const looked = accountAgents?.account === account ? accountAgents : null
  const listed = looked?.agents ?? null
  const { refused, mode } = integratorModes(listed?.find((info) => info.name === chosen?.harness), chosen?.harness ?? '', preferredMode)
  const [saving, setSaving] = useState(false)
  const returnFocus = useReturnFocus()
  const key = useRef(newKey())

  const chooseAccount = (next: string) => {
    setAccount(next)
    setAgent((value) => {
      const first = choices.find((choice) => choice.account_member_id === next)
      return choices.some((choice) => choice.account_member_id === next && agentKey(choice) === value) ? value : first ? agentKey(first) : ''
    })
  }

  const submit = async () => {
    if (!chosen || !listed || !mode) return
    setSaving(true)
    setError(null)
    try {
      await client.missionReplaceIntegrator({
        mission_id: mission.id,
        expected_generation: mission.integrator_generation,
        integrator: { account_member_id: account, harness: chosen.harness, mode, ...(chosen.model ? { model: chosen.model } : {}), ...(chosen.effort ? { effort: chosen.effort } : {}) },
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
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Replace integrator</DialogTitle>
          <DialogDescription>
            Starts a new integrator run from one of this swarm&apos;s agents. It picks up the tasks, workers and agent messages where the old one stopped.
          </DialogDescription>
        </DialogHeader>
        <FormField label="Account">
          <Select value={account} onValueChange={chooseAccount}>
            <SelectTrigger>
              <SelectValue placeholder="Choose an account" />
            </SelectTrigger>
            <SelectContent>
              {accountIDs.map((id) => <SelectItem key={id} value={id}>{memberName({ members, mergedInto }, id)}</SelectItem>)}
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="Agent">
          <Select value={agent} onValueChange={setAgent}>
            <SelectTrigger disabled={!options.length}>
              <SelectValue placeholder="Choose an agent" />
            </SelectTrigger>
            <SelectContent>
              {options.map((choice) => (
                <SelectItem key={agentKey(choice)} value={agentKey(choice)}>
                  {agentLabel(agents?.find((info) => info.name === choice.harness), choice.harness)}{choice.model ? ` · ${choice.model}` : ''}{choice.effort ? ` · ${choice.effort}` : ''}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormField>
        {looked?.error && (
          <Callout tone="failed" role="alert" actions={<Button type="button" size="sm" onClick={() => setListAttempt((n) => n + 1)}>Retry</Button>}>
            Listing {memberName({ members, mergedInto }, account)}&apos;s agents failed, so the integrator&apos;s mode cannot be chosen: {looked.error}
          </Callout>
        )}
        {chosen && listed && <ModeControl label="Mode" value={mode} onChange={setPreferredMode} refused={refused} onSetUp={() => { onClose(); navigate('agents') }} />}
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button onClick={() => void submit()} disabled={saving || !account || !chosen || !listed || !mode}>Replace</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** One key per confirmation: a retry after a failure replays the cancel. */
export function CancelSwarm({ mission, client, onClose, onCancelled }: {
  mission: Mission
  client: Api
  onClose: () => void
  onCancelled: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const key = useRef(newKey())
  const returnFocus = useReturnFocus()

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
      <AlertDialogContent {...returnFocus}>
        <AlertDialogHeader>
          <AlertDialogTitle>Cancel this swarm?</AlertDialogTitle>
          <AlertDialogDescription>
            Its workers and integrator run are stopped. This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
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
