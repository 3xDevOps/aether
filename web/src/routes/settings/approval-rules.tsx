import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { RelativeTime } from '@/components/ui/relative-time'
import type { Api } from '@/lib/api'
import type { AssistantRule } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { SettingRow, SettingsSection, TryAgain } from '@/routes/settings/layout'
import { useStore } from '@/store'

/** The member's approval rules, each revoked with one click; the server reads them on every card. */
export function ApprovalRulesSection({ client }: { client: Api }) {
  const [rules, setRules] = useState<AssistantRule[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [revoking, setRevoking] = useState('')
  const [attempt, setAttempt] = useState(0)

  const identity = useStore((s) => s.identityKey)
  const saves = useStore((s) => s.ruleSaves)

  useEffect(() => {
    let live = true
    setRules(null)
    setError(null)
    client.assistantRules().then(
      (r) => live && setRules(r),
      (err: unknown) => live && setError(errorSentence(err)),
    )
    return () => {
      live = false
    }
  }, [client, attempt, identity, saves])

  const revoke = (id: string) => {
    setRevoking(id)
    setError(null)
    client.assistantRuleRevoke(id).then(
      () => {
        if (useStore.getState().identityKey !== identity) return
        setRules((held) => held?.filter((r) => r.id !== id) ?? null)
        // A read started before the revoke may still list the rule.
        setAttempt((n) => n + 1)
      },
      (err: unknown) => useStore.getState().identityKey === identity && setError(errorSentence(err)),
    ).finally(() => setRevoking(''))
  }

  return (
    <SettingsSection title="Approval rules" id="approval-rules" scope="Your account">
      {rules === null && !error && <SettingRow label="Reading your rules…" />}
      {rules?.length === 0 && (
        <SettingRow label="No rules yet" help="Approve a card with Always approve to let the assistant do that kind of action without asking." />
      )}
      {rules?.map((rule) => (
        <SettingRow
          key={rule.id}
          label={rule.wording}
          help={(
            <>
              <p className="break-words">From: {rule.summary}</p>
              <p>
                Created <RelativeTime at={rule.created_at} />
                {' · '}
                {rule.last_used_at ? <>last used <RelativeTime at={rule.last_used_at} />, {rule.uses === 1 ? 'once' : `${rule.uses} times`}</> : 'never used'}
              </p>
            </>
          )}
          control={(
            <Button size="sm" variant="secondary" aria-label={`Revoke ${rule.wording}`} disabled={revoking !== ''} onClick={() => revoke(rule.id)}>
              {revoking === rule.id ? 'Revoking…' : 'Revoke'}
            </Button>
          )}
        >
          {rule.inactive && <Callout tone="needs-you" role="status">Inactive: {rule.inactive}</Callout>}
        </SettingRow>
      ))}
      {error && (
        <SettingRow label="Approval rules" help={<span role="alert" className="text-state-failed">{error}</span>}>
          {rules === null && <TryAgain onClick={() => { setError(null); setAttempt((n) => n + 1) }} />}
        </SettingRow>
      )}
    </SettingsSection>
  )
}
