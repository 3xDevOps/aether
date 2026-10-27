import { useEffect, useState } from 'react'
import { Confirm } from '@/components/confirm'
import { modeLabel } from '@/components/launch/modes'
import { SavedIssues } from '@/components/launch/saved-defaults'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { FallbackPolicy, Member, SavedLaunchDefaults } from '@/lib/types'
import { SettingRow, SettingsSection, TryAgain } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { saveLaunchDefaults, useLaunchDefaults, useLaunchDefaultsRead } from '@/store/launch-defaults'

const fallbackWords: Record<FallbackPolicy, string> = {
  automatic: 'Any account you may launch on',
  ordered: 'Only the listed accounts, in order',
  off: 'Off',
}

const ownAccount = 'own'

export function LaunchDefaultsSection({ client }: { client: Api }) {
  const cap = useCapability()
  const self = useStore((s) => s.info?.member.id ?? '')
  const [attempt, setAttempt] = useState(0)
  const { result, error: readError } = useLaunchDefaultsRead(client, attempt)
  const [accounts, setAccounts] = useState<Member[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [clearing, setClearing] = useState(false)
  const editable = cap.hasMethod('launch.defaults.set')

  useEffect(() => {
    let live = true
    client.accountList().then((access) => live && setAccounts(access.accounts)).catch(() => {})
    return () => { live = false }
  }, [client])

  if (!cap.hasMethod('launch.defaults.get')) return null

  const name = (id: string) => !id || id === self ? 'Your own account' : accounts.find((member) => member.id === id)?.display_name ?? id
  const change = (next: (current: SavedLaunchDefaults) => SavedLaunchDefaults) => {
    setBusy(true)
    setError(null)
    saveLaunchDefaults(next, client).catch((err: unknown) => setError(errorSentence(err))).finally(() => setBusy(false))
  }
  const defaults = result?.defaults ?? {}
  const agents = Object.entries(defaults.agents ?? {}).sort(([a], [b]) => a.localeCompare(b))
  const saved = result !== null && result.revision > 0
  const disabled = !editable || result === null || busy
  const fallback = defaults.fallback
  const accountChoices = accounts.some((member) => member.id === defaults.account_member_id) || !defaults.account_member_id
    ? accounts
    : [...accounts, { id: defaults.account_member_id, display_name: defaults.account_member_id } as Member]

  return (
    <SettingsSection title="Saved defaults" id="saved-launch-defaults" scope="Your account">
      <SettingRow
        label="Last saved"
        help={result === null
          ? readError ? null : 'Reading your saved defaults…'
          : saved
            ? `The launch dialog starts from these on every device. Saved by ${result.source === 'assistant' ? 'the assistant' : 'you'}${result.updated_at ? ` on ${new Date(result.updated_at).toLocaleString()}` : ''}.`
            : 'Nothing saved yet. A launch from the dialog saves its agent, mode and model here.'}
        control={saved && editable && <Button size="sm" variant="secondary" disabled={disabled} onClick={() => setClearing(true)}>Clear</Button>}
      >
        {(error ?? readError) && <Callout tone="failed" role="alert">{error ?? readError}</Callout>}
        {readError && result === null && (
          <TryAgain onClick={() => { useLaunchDefaults.setState({ error: null }); setAttempt((n) => n + 1) }} />
        )}
        {result && <SavedIssues title="Not usable now" issues={result.invalid ?? []} />}
        {result?.unknown?.length ? (
          <ul aria-label="Not checked" className="flex flex-col gap-1 text-ui-sm text-muted">
            {result.unknown.map((issue) => <li key={`${issue.field}:${issue.value}`}>Not checked: {issue.reason}</li>)}
          </ul>
        ) : null}
      </SettingRow>
      {saved && (
        <SettingRow label="Agent" help={defaults.agent ? `New launches preselect ${defaults.agent}.` : 'No agent saved; the dialog picks the newest run\'s agent.'}>
          {agents.length > 0 && (
            <ul aria-label="Saved agent choices" className="flex flex-col gap-1">
              {agents.map(([agent, choice]) => (
                <li key={agent} className="flex min-h-7 items-center gap-2 text-ui coarse:min-h-11">
                  <span className="min-w-0 flex-1 truncate">
                    <span className="font-medium">{agent}</span>
                    <span className="text-muted"> · {choice.mode ? modeLabel(choice.mode) : 'Default mode'} · {choice.model ? choice.model : 'Default model'}</span>
                  </span>
                  {editable && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={disabled}
                      aria-label={`Forget ${agent}`}
                      onClick={() => change((current) => {
                        const rest = { ...current.agents }
                        delete rest[agent]
                        return { ...current, agents: rest, ...(current.agent === agent ? { agent: '' } : {}) }
                      })}
                    >
                      Forget
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </SettingRow>
      )}
      <SettingRow
        label="Account"
        labelFor="settings-launch-account"
        help="The account a launch starts on unless you pick another. A teammate's account works only while they share it with you."
        control={(
          <Select
            value={defaults.account_member_id || ownAccount}
            disabled={disabled}
            onValueChange={(next) => change((current) => ({ ...current, account_member_id: next === ownAccount ? '' : next }))}
          >
            <SelectTrigger id="settings-launch-account" className="w-56 max-w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ownAccount}>Your own account</SelectItem>
              {accountChoices.filter((member) => member.id !== self).map((member) => (
                <SelectItem key={member.id} value={member.id}>{name(member.id)}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      />
      <SettingRow
        label="Fallback"
        labelFor="settings-launch-fallback"
        help={fallback?.policy === 'ordered'
          ? `When usage runs out, a launch moves to ${(fallback.order ?? []).map(name).join(', then ')}. Reorder the list in the launch dialog under Options.`
          : 'Where a launch moves when its account\'s usage runs out. Order the accounts in the launch dialog under Options, then Save as my default.'}
        control={(
          <Select
            value={fallback?.policy ?? 'automatic'}
            disabled={disabled}
            onValueChange={(next) => change((current) => ({ ...current, fallback: next === 'ordered' ? current.fallback : { policy: next as FallbackPolicy } }))}
          >
            <SelectTrigger id="settings-launch-fallback" className="w-56 max-w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(['automatic', 'off', ...(fallback?.policy === 'ordered' ? ['ordered' as const] : [])] as FallbackPolicy[]).map((policy) => (
                <SelectItem key={policy} value={policy}>{fallbackWords[policy]}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      />
      {clearing && (
        <Confirm
          title="Clear your saved defaults?"
          description="The launch dialog on every device goes back to Aether's own defaults until a launch saves new ones."
          action="Clear"
          pending="Clearing…"
          onConfirm={() => saveLaunchDefaults(() => null, client)}
          onDone={() => {}}
          onClose={() => setClearing(false)}
        />
      )}
    </SettingsSection>
  )
}
