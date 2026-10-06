import { useRef, useState } from 'react'
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
import type { Mission } from '@/lib/types'
import { useStore } from '@/store'

function newKey(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `mission-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

/** mission.replace-integrator takes any execution choice's account and agent and runs it Standard. */
export function ReplaceIntegrator({ mission, client, onClose, onReplaced }: {
  mission: Mission
  client: Api
  onClose: () => void
  onReplaced: () => void
}) {
  const members = useStore((s) => s.members)
  const choices = mission.execution_choices.filter(
    (choice, index, all) =>
      all.findIndex((other) => other.account_member_id === choice.account_member_id && other.harness === choice.harness) === index,
  )
  const accountIDs = [...new Set(choices.map((choice) => choice.account_member_id))]
  const current =
    choices.find(
      (choice) => choice.account_member_id === mission.integrator.account_member_id && choice.harness === mission.integrator.harness,
    ) ?? choices[0]
  const [account, setAccount] = useState(current?.account_member_id ?? '')
  const [harness, setHarness] = useState(current?.harness ?? '')
  const harnesses = choices.filter((choice) => choice.account_member_id === account).map((choice) => choice.harness)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const key = useRef(newKey())

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
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Replace integrator</DialogTitle>
          <DialogDescription>
            Starts a new integrator run in Standard from one of this swarm&apos;s agents. It picks up the tasks, workers and agent messages where the old one stopped.
          </DialogDescription>
        </DialogHeader>
        <FormField label="Account">
          <Select value={account} onValueChange={chooseAccount}>
            <SelectTrigger>
              <SelectValue placeholder="Choose an account" />
            </SelectTrigger>
            <SelectContent>
              {accountIDs.map((id) => <SelectItem key={id} value={id}>{members[id]?.display_name ?? id}</SelectItem>)}
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="Agent">
          <Select value={harness} onValueChange={setHarness}>
            <SelectTrigger disabled={!harnesses.length}>
              <SelectValue placeholder="Choose an agent" />
            </SelectTrigger>
            <SelectContent>
              {harnesses.map((name) => <SelectItem key={name} value={name}>{name}</SelectItem>)}
            </SelectContent>
          </Select>
        </FormField>
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button onClick={() => void submit()} disabled={saving || !account || !harness}>Replace</Button>
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
      <AlertDialogContent>
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
